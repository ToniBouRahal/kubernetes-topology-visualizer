package collector

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
	"golang.org/x/sys/unix"
)

// Stat indices. Must stay in step with enum stat_index in bpf/tcp_connect.bpf.c.
const (
	statEventsSubmitted uint32 = iota
	statRingbufDropped
	statFilteredFamily
	statFilteredProtocol
	statFilteredTransition
	statTrackingMissed
	statTrackingFailed
	statMax
)

// EventSchemaVersion mirrors EVENT_SCHEMA_VERSION in the BPF program. A mismatch means the
// loaded object was built from different source than this binary.
const EventSchemaVersion = 2

// Event is one observed active-open TCP connection, decoded into domain types.
//
// Addresses are netip.Addr built directly from the raw 4-byte network-order arrays. No
// endianness conversion happens anywhere in this path — see ADR-002 D-2.1 (C4).
type Event struct {
	Timestamp        time.Time
	PID              uint32
	SrcIP            netip.Addr
	DstIP            netip.Addr
	SrcPort          uint16
	DstPort          uint16
	Failed           bool
	ConnectLatencyUS *int64
}

// Stats are the kernel-side counters. RingbufDropped is the only signal of lost events: unlike
// a perf array, a ring buffer reports no lost-sample count to the consumer, so without this
// counter data loss would be silent (ADR-002 D-2.2).
type Stats struct {
	EventsSubmitted    uint64
	RingbufDropped     uint64
	FilteredFamily     uint64
	FilteredProtocol   uint64
	FilteredTransition uint64
	TrackingMissed     uint64
	TrackingFailed     uint64
}

// Collector owns the BPF objects, the tracepoint attachment, and the ring-buffer reader.
type Collector struct {
	objs   TcpConnectObjects
	tp     link.Link
	reader *ringbuf.Reader

	// Reads the wall clock and CLOCK_MONOTONIC together; nil means the system clocks. A field so
	// a test can stop the monotonic clock the way a suspend does.
	clock func() (wall time.Time, mono time.Duration)
}

// New loads the BPF program, attaches it to sock/inet_sock_set_state, and opens the ring buffer.
//
// Failure messages name the likely cause, because the two realistic ones — missing BTF and
// insufficient privileges — are both environment problems that Phase 5 requires to be
// actionable rather than a bare errno (ADR-001 §7).
func New() (_ *Collector, err error) {
	// Harmless on kernels with BPF memcg accounting (5.11+); required below that.
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("remove memlock rlimit (needs CAP_SYS_RESOURCE or a privileged container): %w", err)
	}

	c := &Collector{}

	// One cleanup path rather than an unwinding ladder at every step. Close is safe on a
	// partially built Collector: the zero-value BPF objects and the nil-guarded reader and
	// link all no-op.
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()

	if err = LoadTcpConnectObjects(&c.objs, nil); err != nil {
		return nil, fmt.Errorf(
			"load BPF objects (check BTF at /sys/kernel/btf/vmlinux and that the container is privileged): %w", err)
	}

	if c.tp, err = link.Tracepoint("sock", "inet_sock_set_state", c.objs.TraceInetSockSetState, nil); err != nil {
		return nil, fmt.Errorf("attach tracepoint sock/inet_sock_set_state: %w", err)
	}

	if c.reader, err = ringbuf.NewReader(c.objs.Events); err != nil {
		return nil, fmt.Errorf("open ring buffer reader: %w", err)
	}

	return c, nil
}

// systemClock reads the wall clock and CLOCK_MONOTONIC, back to back.
func systemClock() (time.Time, time.Duration) {
	var ts unix.Timespec
	// CLOCK_MONOTONIC cannot fail on Linux; on error the zero reading only skews this event.
	_ = unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts)
	return time.Now(), time.Duration(ts.Nano())
}

// wallTime converts an event's bpf_ktime_get_ns() reading (CLOCK_MONOTONIC) to wall-clock time:
// now, minus how long ago on the monotonic clock the event happened.
//
// Measured against the clocks as they read NOW, not against an epoch fixed at start-up.
// CLOCK_MONOTONIC stops while the machine is suspended, so a fixed epoch falls behind by the
// length of every suspend: after a laptop slept for 83 minutes, every event was stamped 83 minutes
// in the past and the live graph went empty until the agent restarted. Reading both clocks per
// event costs two vDSO calls — tens of nanoseconds, well under a millisecond a second at the
// 1,000 events/s/node target (ADR-001 §6) — and also follows NTP adjustments to the wall clock.
func (c *Collector) wallTime(ktimeNs uint64) time.Time {
	read := c.clock
	if read == nil {
		read = systemClock
	}
	wall, mono := read()
	return wall.Add(time.Duration(ktimeNs) - mono)
}

// Run drains the ring buffer until ctx is cancelled, invoking handle for each decoded event.
//
// handle runs on the reader goroutine and must not block: anything slow here backs up the ring
// buffer and shows up as RingbufDropped.
func (c *Collector) Run(ctx context.Context, handle func(Event)) error {
	// Unblock the blocking Read below on cancellation.
	go func() {
		<-ctx.Done()
		_ = c.reader.Close()
	}()

	for {
		record, err := c.reader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read ring buffer: %w", err)
		}

		event, err := c.decode(record.RawSample)
		if err != nil {
			// A malformed record means the loaded object disagrees with this binary.
			// Surfacing it is better than silently skipping.
			return fmt.Errorf("decode event: %w", err)
		}
		handle(event)
	}
}

// decode converts a raw ring-buffer record into a domain Event.
func (c *Collector) decode(raw []byte) (Event, error) {
	if len(raw) < int(eventSize) {
		return Event{}, fmt.Errorf("record is %d bytes, expected %d", len(raw), eventSize)
	}

	e := unmarshalEvent(raw)

	if e.Version != EventSchemaVersion {
		return Event{}, fmt.Errorf(
			"event schema version %d, this binary expects %d; rebuild with `make generate`",
			e.Version, EventSchemaVersion)
	}

	if e.Outcome > 1 || e.DurationKnown > 1 || e.DurationUs > uint64(1<<63-1) || (e.Outcome == 1 && e.DurationKnown != 0) || (e.DurationKnown == 0 && e.DurationUs != 0) {
		return Event{}, fmt.Errorf("invalid connection outcome or timing flags")
	}
	var latency *int64
	if e.DurationKnown == 1 {
		micros := int64(e.DurationUs)
		latency = &micros
	}
	return Event{
		Failed:           e.Outcome == 1,
		ConnectLatencyUS: latency,
		Timestamp:        c.wallTime(e.TimestampNs),
		PID:              e.Pid,
		// AddrFrom4 takes the network-order bytes as-is. No byte swapping: the bytes are
		// already in the order an IPv4 address is written.
		SrcIP:   netip.AddrFrom4(e.Saddr),
		DstIP:   netip.AddrFrom4(e.Daddr),
		SrcPort: e.Sport,
		DstPort: e.Dport,
	}, nil
}

// Stats reads the kernel-side counters.
func (c *Collector) Stats() (Stats, error) {
	values := make([]uint64, statMax)
	for i := uint32(0); i < statMax; i++ {
		if err := c.objs.Stats.Lookup(&i, &values[i]); err != nil {
			return Stats{}, fmt.Errorf("read stat %d: %w", i, err)
		}
	}
	return Stats{
		EventsSubmitted:    values[statEventsSubmitted],
		RingbufDropped:     values[statRingbufDropped],
		FilteredFamily:     values[statFilteredFamily],
		FilteredProtocol:   values[statFilteredProtocol],
		FilteredTransition: values[statFilteredTransition],
		TrackingMissed:     values[statTrackingMissed],
		TrackingFailed:     values[statTrackingFailed],
	}, nil
}

// Close detaches the program and releases the BPF objects.
func (c *Collector) Close() error {
	var errs []error
	if c.reader != nil {
		if err := c.reader.Close(); err != nil && !errors.Is(err, ringbuf.ErrClosed) {
			errs = append(errs, err)
		}
	}
	if c.tp != nil {
		errs = append(errs, c.tp.Close())
	}
	errs = append(errs, c.objs.Close())
	return errors.Join(errs...)
}
