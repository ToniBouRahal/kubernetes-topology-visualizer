//go:build privileged

// Privileged eBPF tests — T-2.11 and T-2.12.
//
// These load and attach a real BPF program, so they need root (or CAP_BPF + CAP_PERFMON) and a
// kernel with BTF. They are separated from ordinary CI by the `privileged` build tag but must run
// before release (ADR-001 §8, ADR-008 D-8.1).
//
//	make test-ebpf
package collector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"
)

func requireBTF(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err != nil {
		t.Skipf("no BTF at /sys/kernel/btf/vmlinux: %v", err)
	}
}

// newPrivilegedCollector attaches the real program, skipping if privileges are missing rather
// than failing — an unprivileged developer running the whole suite should get a clear skip.
func newPrivilegedCollector(t *testing.T) *Collector {
	t.Helper()
	requireBTF(t)

	c, err := New()
	if err != nil {
		if errors.Is(err, os.ErrPermission) || os.Geteuid() != 0 {
			t.Skipf("needs root or CAP_BPF (run `make test-ebpf`): %v", err)
		}
		t.Fatalf("attach collector: %v", err)
	}
	return c
}

// collect runs the collector for the given window and returns everything it captured.
func collect(t *testing.T, c *Collector, window time.Duration, generate func()) []Event {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu      sync.Mutex
		events  []Event
		runErr  error
		running sync.WaitGroup
	)

	running.Add(1)
	go func() {
		defer running.Done()
		runErr = c.Run(ctx, func(ev Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		})
	}()

	// Let the reader settle before generating traffic.
	time.Sleep(150 * time.Millisecond)
	generate()
	time.Sleep(window)

	cancel()
	running.Wait()

	if runErr != nil {
		t.Fatalf("collector run: %v", runErr)
	}

	mu.Lock()
	defer mu.Unlock()
	out := make([]Event, len(events))
	copy(out, events)
	return out
}

// T-2.11: a real TCP connection produces a captured IPv4 event.
//
// This is the feasibility claim of the whole project reduced to one assertion: an unmodified
// process opens a socket, and the agent sees it without that process being instrumented.
func TestPrivilegedCapturesRealConnection(t *testing.T) {
	c := newPrivilegedCollector(t)
	defer c.Close()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	port := uint16(listener.Addr().(*net.TCPAddr).Port)

	events := collect(t, c, 1500*time.Millisecond, func() {
		conn, err := net.DialTimeout("tcp4", listener.Addr().String(), 2*time.Second)
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		conn.Close()
	})

	var matched []Event
	for _, ev := range events {
		if ev.DstPort == port {
			matched = append(matched, ev)
		}
	}

	if len(matched) == 0 {
		t.Fatalf("no event captured for a real connection to port %d (saw %d unrelated events)",
			port, len(events))
	}

	ev := matched[0]
	if !ev.DstIP.IsLoopback() {
		t.Errorf("DstIP = %s, want a loopback address", ev.DstIP)
	}
	if ev.SrcPort == 0 {
		t.Error("SrcPort is zero; the tracepoint should provide the ephemeral source port")
	}
	if ev.Timestamp.IsZero() || time.Since(ev.Timestamp) > time.Minute {
		t.Errorf("Timestamp %v is not a plausible wall-clock time", ev.Timestamp)
	}
}

// T-2.3 at runtime, and the single most important correctness property in the collector.
//
// A loopback connection creates TWO sockets that both reach ESTABLISHED: the client's, from
// SYN_SENT, and the server's accepted socket, from SYN_RECV. A filter that matches only on
// newstate — as the reference prototype does — captures both, and the accepted one becomes a
// false reverse edge.
//
// Exactly one event per connection is the proof that the oldstate check works.
func TestPrivilegedProducesNoFalseReverseEdge(t *testing.T) {
	c := newPrivilegedCollector(t)
	defer c.Close()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{}, 8)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			conn.Close()
		}
	}()

	listenPort := uint16(listener.Addr().(*net.TCPAddr).Port)
	const connections = 3

	events := collect(t, c, 2*time.Second, func() {
		for i := 0; i < connections; i++ {
			conn, err := net.DialTimeout("tcp4", listener.Addr().String(), 2*time.Second)
			if err != nil {
				t.Errorf("dial %d: %v", i, err)
				return
			}
			<-accepted
			conn.Close()
			time.Sleep(50 * time.Millisecond)
		}
	})

	var toListener, fromListener int
	for _, ev := range events {
		switch {
		case ev.DstPort == listenPort:
			toListener++
		case ev.SrcPort == listenPort:
			// The accepted server socket. Its "destination" is the client's ephemeral port,
			// so this is the reverse edge that must never be emitted.
			fromListener++
		}
	}

	if fromListener != 0 {
		t.Errorf("captured %d event(s) originating FROM the listening port — accepted server "+
			"sockets are being recorded as active opens, which produces false reverse edges. "+
			"Check the oldstate == TCP_SYN_SENT condition in bpf/tcp_connect.bpf.c", fromListener)
	}
	if toListener != connections {
		t.Errorf("captured %d client-side events, want exactly %d (one per connection)",
			toListener, connections)
	}
}

// Only active opens count: a connection that is reused produces no further events. This is why
// the UI must say "connections", never "requests" (ADR-001 §10).
func TestPrivilegedReusedConnectionEmitsNoFurtherEvents(t *testing.T) {
	c := newPrivilegedCollector(t)
	defer c.Close()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 16)
				for {
					if _, err := conn.Read(buf); err != nil {
						conn.Close()
						return
					}
					conn.Write([]byte("ok"))
				}
			}()
		}
	}()

	port := uint16(listener.Addr().(*net.TCPAddr).Port)

	events := collect(t, c, 1500*time.Millisecond, func() {
		conn, err := net.DialTimeout("tcp4", listener.Addr().String(), 2*time.Second)
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		defer conn.Close()
		// Ten round trips over ONE connection.
		buf := make([]byte, 16)
		for i := 0; i < 10; i++ {
			if _, err := conn.Write([]byte("ping")); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
			conn.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := conn.Read(buf); err != nil {
				t.Errorf("read %d: %v", i, err)
				return
			}
		}
	})

	var count int
	for _, ev := range events {
		if ev.DstPort == port {
			count++
		}
	}
	if count != 1 {
		t.Errorf("captured %d events for one connection carrying ten round trips, want 1. "+
			"Connection establishments are not requests.", count)
	}
}

// T-2.12: the drop counter is wired and readable.
//
// A burst well past the ring buffer capacity is not reliably reproducible on an idle machine,
// so this asserts the counter is readable and consistent rather than forcing a drop. What
// matters is that a drop would be *visible*: the ring buffer reports nothing to userspace, so
// an unwired counter means silent loss.
func TestPrivilegedStatsAreReadable(t *testing.T) {
	c := newPrivilegedCollector(t)
	defer c.Close()

	before, err := c.Stats()
	if err != nil {
		t.Fatalf("read stats: %v", err)
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	const burst = 200
	collect(t, c, 2*time.Second, func() {
		for i := 0; i < burst; i++ {
			conn, err := net.DialTimeout("tcp4", listener.Addr().String(), time.Second)
			if err != nil {
				continue
			}
			conn.Close()
		}
	})

	after, err := c.Stats()
	if err != nil {
		t.Fatalf("read stats after burst: %v", err)
	}

	if after.EventsSubmitted <= before.EventsSubmitted {
		t.Errorf("EventsSubmitted did not advance: %d -> %d",
			before.EventsSubmitted, after.EventsSubmitted)
	}

	// The transition filter must be doing real work: a live machine has far more state changes
	// than active opens.
	if after.FilteredTransition <= before.FilteredTransition {
		t.Error("FilteredTransition did not advance; the four-condition filter appears inactive")
	}

	t.Logf("submitted=%d dropped=%d filtered(family=%d protocol=%d transition=%d)",
		after.EventsSubmitted, after.RingbufDropped,
		after.FilteredFamily, after.FilteredProtocol, after.FilteredTransition)

	if after.RingbufDropped > 0 {
		t.Logf("note: %d events dropped under a %d-connection burst — this is the counter "+
			"working, and is what the kernel_samples_lost metric exposes", after.RingbufDropped, burst)
	}
}

// The privacy invariant, checked against a live program: no event carries anything but L3/L4
// metadata. Payload bytes must not exist anywhere in the pipeline (ADR-001 §6).
func TestPrivilegedEventsCarryNoPayload(t *testing.T) {
	c := newPrivilegedCollector(t)
	defer c.Close()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	secret := "SUPER-SECRET-PAYLOAD-MARKER"
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			io := make([]byte, 256)
			conn.Read(io)
			conn.Write([]byte(secret))
			conn.Close()
		}
	}()

	events := collect(t, c, 1500*time.Millisecond, func() {
		conn, err := net.DialTimeout("tcp4", listener.Addr().String(), 2*time.Second)
		if err != nil {
			t.Errorf("dial: %v", err)
			return
		}
		defer conn.Close()
		conn.Write([]byte(secret))
		buf := make([]byte, 256)
		conn.SetReadDeadline(time.Now().Add(time.Second))
		conn.Read(buf)
	})

	for _, ev := range events {
		rendered := fmt.Sprintf("%+v", ev)
		if len(rendered) > 0 && contains(rendered, secret) {
			t.Fatalf("an event carried payload content: %s", rendered)
		}
	}

	// Structural: the decoded event exposes only L3/L4 metadata and a timestamp.
	if len(events) > 0 {
		ev := events[0]
		if !ev.SrcIP.IsValid() || !ev.DstIP.IsValid() {
			t.Error("addresses should be valid")
		}
		var _ netip.Addr = ev.SrcIP
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
