package collector

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"
	"unsafe"
)

// T-2.1: the C struct, the bpf2go mirror, and our decoding view must agree byte for byte.
//
// This is the assertion that makes the whole decode path trustworthy. If someone adds a field
// to the C struct and forgets to regenerate, or edits rawEvent without touching the C, this
// fails rather than silently shifting every field.
func TestEventLayoutMatchesGeneratedStruct(t *testing.T) {
	if got, want := eventSize, uintptr(32); got != want {
		t.Errorf("eventSize = %d, want %d", got, want)
	}

	// The generated mirror carries a zero-width structs.HostLayout marker, so its size is the
	// authority and rawEvent must not exceed it.
	if unsafe.Sizeof(rawEvent{}) > eventSize {
		t.Errorf("rawEvent (%d bytes) is larger than the BPF event (%d bytes)",
			unsafe.Sizeof(rawEvent{}), eventSize)
	}

	generated := reflect.TypeOf(TcpConnectEvent{})
	decoding := reflect.TypeOf(rawEvent{})

	// Every field of the decoding view must exist in the generated mirror at the same offset.
	for i := 0; i < decoding.NumField(); i++ {
		f := decoding.Field(i)
		g, ok := generated.FieldByName(f.Name)
		if !ok {
			t.Errorf("rawEvent field %q has no counterpart in the generated struct", f.Name)
			continue
		}
		if g.Offset != f.Offset {
			t.Errorf("field %q at offset %d in rawEvent but %d in the generated struct",
				f.Name, f.Offset, g.Offset)
		}
		if g.Type.Size() != f.Type.Size() {
			t.Errorf("field %q is %d bytes in rawEvent but %d in the generated struct",
				f.Name, f.Type.Size(), g.Type.Size())
		}
	}
}

// T-2.1: the named offsets used by the decoder must match the real struct offsets.
func TestFieldOffsetsMatchDecoder(t *testing.T) {
	typ := reflect.TypeOf(rawEvent{})
	for _, tc := range []struct {
		field string
		want  uintptr
	}{
		{"TimestampNs", offTimestampNs},
		{"Pid", offPid},
		{"Saddr", offSaddr},
		{"Daddr", offDaddr},
		{"Sport", offSport},
		{"Dport", offDport},
		{"Family", offFamily},
		{"Protocol", offProtocol},
		{"Version", offVersion},
	} {
		f, ok := typ.FieldByName(tc.field)
		if !ok {
			t.Fatalf("rawEvent has no field %q", tc.field)
		}
		if f.Offset != tc.want {
			t.Errorf("%s: struct offset %d, decoder constant %d", tc.field, f.Offset, tc.want)
		}
	}
}

// T-2.2: addresses survive decoding with no byte swapping.
//
// The reference prototype packed the 4 address bytes into a __u32 and read them back with
// binary.LittleEndian — a double reversal that produces the right answer only on a
// little-endian host, by accident (ADR-002 C4). These cases would catch that inversion.
func TestAddressDecodingPreservesByteOrder(t *testing.T) {
	cases := []struct {
		name    string
		src     [4]byte
		dst     [4]byte
		wantSrc string
		wantDst string
	}{
		{"cluster pod IPs", [4]byte{10, 244, 1, 7}, [4]byte{10, 244, 2, 11}, "10.244.1.7", "10.244.2.11"},
		{"asymmetric octets", [4]byte{1, 2, 3, 4}, [4]byte{4, 3, 2, 1}, "1.2.3.4", "4.3.2.1"},
		{"high bit set", [4]byte{192, 168, 0, 1}, [4]byte{140, 82, 121, 4}, "192.168.0.1", "140.82.121.4"},
		{"broadcast-ish", [4]byte{255, 255, 255, 0}, [4]byte{0, 0, 0, 1}, "255.255.255.0", "0.0.0.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := make([]byte, eventSize)
			copy(raw[offSaddr:], tc.src[:])
			copy(raw[offDaddr:], tc.dst[:])
			raw[offVersion] = EventSchemaVersion

			c := &Collector{}
			ev, err := c.decode(raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := ev.SrcIP.String(); got != tc.wantSrc {
				t.Errorf("SrcIP = %s, want %s", got, tc.wantSrc)
			}
			if got := ev.DstIP.String(); got != tc.wantDst {
				t.Errorf("DstIP = %s, want %s", got, tc.wantDst)
			}
			if !ev.SrcIP.Is4() {
				t.Error("SrcIP should be a 4-byte address")
			}
		})
	}
}

func TestDecodePortsAndVersion(t *testing.T) {
	raw := make([]byte, eventSize)
	raw[offSport] = 0xD2 // 54322 little-endian
	raw[offSport+1] = 0xD4
	raw[offDport] = 0x8F // 6543... use explicit expectation below
	raw[offDport+1] = 0x19
	raw[offVersion] = EventSchemaVersion
	copy(raw[offSaddr:], []byte{10, 0, 0, 1})
	copy(raw[offDaddr:], []byte{10, 0, 0, 2})

	c := &Collector{}
	ev, err := c.decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ev.SrcPort != 0xD4D2 {
		t.Errorf("SrcPort = %d, want %d", ev.SrcPort, 0xD4D2)
	}
	if ev.DstPort != 0x198F {
		t.Errorf("DstPort = %d, want %d", ev.DstPort, 0x198F)
	}
}

func TestDecodeRejectsShortRecord(t *testing.T) {
	c := &Collector{}
	if _, err := c.decode(make([]byte, 8)); err == nil {
		t.Error("expected an error for a truncated record")
	}
}

// A schema mismatch means the loaded object was built from different source than this binary.
// Failing loudly beats decoding garbage into a plausible-looking edge.
func TestDecodeRejectsWrongSchemaVersion(t *testing.T) {
	raw := make([]byte, eventSize)
	raw[offVersion] = EventSchemaVersion + 1
	c := &Collector{}
	if _, err := c.decode(raw); err == nil {
		t.Error("expected an error for a mismatched event schema version")
	}
}

func TestDecodeTimestampIsOffsetFromMonotonicEpoch(t *testing.T) {
	epoch := mustTime(t, "2026-08-12T09:00:00Z")
	c := &Collector{monotonicEpoch: epoch}

	raw := make([]byte, eventSize)
	raw[offVersion] = EventSchemaVersion
	// 90 seconds in nanoseconds, little-endian.
	for i, b := range []byte{0x00, 0x90, 0x2F, 0xF0, 0x14, 0x00, 0x00, 0x00} {
		raw[offTimestampNs+i] = b
	}

	ev, err := c.decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ev.Timestamp.After(epoch) {
		t.Errorf("timestamp %v should be after the epoch %v", ev.Timestamp, epoch)
	}
}

// ── Source-level invariants ────────────────────────────────────────────────────────────────
// These read the BPF C source. They cannot replace the privileged runtime tests (T-2.11,
// T-2.12), but they do catch the specific regressions that would be most costly to discover
// during a demo, and they run in ordinary CI.

func bpfSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "bpf", "tcp_connect.bpf.c")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read BPF source: %v", err)
	}
	return string(b)
}

var (
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment  = regexp.MustCompile(`//[^\n]*`)
)

// bpfCode returns the source with comments removed.
//
// The forbidden-symbol scan below is about what the program *does*, so the comments that
// explain the invariant must not trip the check that enforces it.
func bpfCode(t *testing.T) string {
	t.Helper()
	src := blockComment.ReplaceAllString(bpfSource(t), "")
	return lineComment.ReplaceAllString(src, "")
}

// T-2.3: the active-open filter must test BOTH oldstate and newstate.
//
// Matching on newstate alone also captures accepted server sockets, which reach ESTABLISHED
// from SYN_RECV — producing a false reverse edge for every inbound connection. This is the
// defect ADR-001 §3 records in the reference prototype and the single most important
// correctness rule in the collector.
func TestBPFFiltersOnBothStates(t *testing.T) {
	src := bpfSource(t)

	if !regexp.MustCompile(`oldstate\s*!=\s*TCP_SYN_SENT`).MatchString(src) {
		t.Error("BPF program does not filter on oldstate == TCP_SYN_SENT; " +
			"accepted server sockets will produce false reverse edges")
	}
	if !regexp.MustCompile(`newstate\s*!=\s*TCP_ESTABLISHED`).MatchString(src) {
		t.Error("BPF program does not filter on newstate == TCP_ESTABLISHED")
	}
}

// T-2.4: only AF_INET / IPPROTO_TCP are accepted.
func TestBPFRejectsOtherFamiliesAndProtocols(t *testing.T) {
	src := bpfSource(t)
	for _, want := range []string{`family\s*!=\s*AF_INET`, `protocol\s*!=\s*IPPROTO_TCP`} {
		if !regexp.MustCompile(want).MatchString(src) {
			t.Errorf("BPF program is missing the guard %q", want)
		}
	}
}

// T-2.7: no payload bytes anywhere.
//
// A structural assertion rather than a behavioural one: the guarantee is that no code path
// exists which could read payload, so the check is that no such call is present at all
// (ADR-001 §6).
func TestBPFNeverReadsPayload(t *testing.T) {
	src := bpfCode(t)

	forbidden := []struct {
		pattern string
		why     string
	}{
		{`bpf_probe_read\s*\(`, "probe reads can copy arbitrary kernel memory including payload"},
		{`bpf_probe_read_kernel\s*\(`, "same"},
		{`bpf_probe_read_user\s*\(`, "reads user memory"},
		{`bpf_skb_load_bytes`, "loads packet bytes"},
		{`\bpayload\b`, "a payload-named symbol has no business in this program"},
	}
	for _, f := range forbidden {
		if regexp.MustCompile(f.pattern).MatchString(src) {
			t.Errorf("BPF source matches %q — %s. ADR-001 §6 forbids capturing payload bytes.",
				f.pattern, f.why)
		}
	}

	// The event struct must carry no variable-length or buffer-shaped field.
	typ := reflect.TypeOf(TcpConnectEvent{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() == reflect.Slice || f.Type.Kind() == reflect.Pointer {
			t.Errorf("event field %q is a %s; the event must stay fixed-size and payload-free",
				f.Name, f.Type.Kind())
		}
		if f.Type.Kind() == reflect.Array && f.Type.Len() > 16 {
			t.Errorf("event field %q is a %d-byte array — too large to be an address or padding",
				f.Name, f.Type.Len())
		}
	}
}

// The ring buffer must be used, not a perf event array (ADR-002 D-2.1 C2), and drops must be
// counted — the ring buffer gives userspace no lost-sample count of its own (D-2.2).
func TestBPFUsesRingBufferAndCountsDrops(t *testing.T) {
	src := bpfSource(t)

	if !regexp.MustCompile(`BPF_MAP_TYPE_RINGBUF`).MatchString(src) {
		t.Error("expected BPF_MAP_TYPE_RINGBUF")
	}
	if regexp.MustCompile(`BPF_MAP_TYPE_PERF_EVENT_ARRAY`).MatchString(src) {
		t.Error("perf event array found; ADR-002 D-2.1 requires a ring buffer")
	}
	if !regexp.MustCompile(`(?s)bpf_ringbuf_reserve.*?STAT_RINGBUF_DROPPED`).MatchString(src) {
		t.Error("a failed bpf_ringbuf_reserve must increment STAT_RINGBUF_DROPPED, " +
			"otherwise lost events are invisible to userspace")
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return parsed
}
