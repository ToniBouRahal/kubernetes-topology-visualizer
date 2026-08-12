package collector

import (
	"encoding/binary"
	"unsafe"
)

// eventSize is the wire size of `struct event` in bpf/tcp_connect.bpf.c.
//
// Derived from the bpf2go-generated mirror rather than written by hand, so a change to the C
// struct moves this automatically once `make generate` runs. Test T-2.1 pins the value.
const eventSize = unsafe.Sizeof(TcpConnectEvent{})

// rawEvent is the decoding view of `struct event`.
//
// It deliberately does not reuse TcpConnectEvent directly for decoding: that type carries a
// structs.HostLayout marker for the ebpf loader's benefit, and reflective decoding of it would
// be both slower and less explicit than the field-by-field reads below. The two are asserted
// equivalent by test T-2.1.
type rawEvent struct {
	TimestampNs uint64
	Pid         uint32
	Saddr       [4]byte
	Daddr       [4]byte
	Sport       uint16
	Dport       uint16
	Family      uint8
	Protocol    uint8
	Version     uint8
}

// Field offsets within the 32-byte record. Named rather than inlined so the test can assert
// against them independently of the decoder.
const (
	offTimestampNs = 0
	offPid         = 8
	offSaddr       = 12
	offDaddr       = 16
	offSport       = 20
	offDport       = 22
	offFamily      = 24
	offProtocol    = 25
	offVersion     = 26
)

// unmarshalEvent decodes one ring-buffer record.
//
// Multi-byte integers are read little-endian because the BPF program stores them in native
// host order and this agent targets amd64 only (bpf2go emits *_bpfel bindings; IPv6 and other
// architectures are out of scope — ADR-001 §4.2).
//
// The address fields are NOT run through any byte-order conversion. They arrive from the
// kernel as 4 bytes in network order, which is already the order an IPv4 address is written,
// and they stay that way all the way to netip.AddrFrom4.
func unmarshalEvent(b []byte) rawEvent {
	return rawEvent{
		TimestampNs: binary.LittleEndian.Uint64(b[offTimestampNs : offTimestampNs+8]),
		Pid:         binary.LittleEndian.Uint32(b[offPid : offPid+4]),
		Saddr:       [4]byte(b[offSaddr : offSaddr+4]),
		Daddr:       [4]byte(b[offDaddr : offDaddr+4]),
		Sport:       binary.LittleEndian.Uint16(b[offSport : offSport+2]),
		Dport:       binary.LittleEndian.Uint16(b[offDport : offDport+2]),
		Family:      b[offFamily],
		Protocol:    b[offProtocol],
		Version:     b[offVersion],
	}
}
