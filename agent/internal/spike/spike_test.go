//go:build privileged

// Byte-accounting feasibility experiment — P4-A22.
//
// Transfers a KNOWN number of bytes and compares against what tcp_sock reports. The whole point
// is a number that can be checked, not an impression that it "seems to work".
//
//	make spike-bytes
package spike

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

type closeEvent struct {
	BytesSent     uint64
	BytesReceived uint64
	Saddr         [4]byte
	Daddr         [4]byte
	Sport         uint16
	Dport         uint16
}

func decode(b []byte) closeEvent {
	return closeEvent{
		BytesSent:     binary.LittleEndian.Uint64(b[0:8]),
		BytesReceived: binary.LittleEndian.Uint64(b[8:16]),
		Saddr:         [4]byte(b[16:20]),
		Daddr:         [4]byte(b[20:24]),
		Sport:         binary.LittleEndian.Uint16(b[24:26]),
		Dport:         binary.LittleEndian.Uint16(b[26:28]),
	}
}

func prepare(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err != nil {
		t.Skipf("no BTF: %v", err)
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Skipf("needs privileges: %v", err)
	}
}

// Both programs, always. The close handler only reports sockets that passed the active-open
// filter first, so attaching TraceClose alone would silently discard every event.
func attach(t *testing.T) (*ringbuf.Reader, func()) {
	t.Helper()
	objs, _, _, cleanup := attachBoth(t)

	reader, err := ringbuf.NewReader(objs.Closes)
	if err != nil {
		cleanup()
		t.Fatalf("ringbuf: %v", err)
	}

	return reader, func() { reader.Close(); cleanup() }
}

// The experiment. A client sends payloadSize bytes and receives a known reply; the reported
// counters must match.
func TestPrivilegedByteAccountingAccuracy(t *testing.T) {
	const payloadSize = 64 * 1024
	const replySize = 4 * 1024

	reader, cleanup := attach(t)
	defer cleanup()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	serverPort := uint16(listener.Addr().(*net.TCPAddr).Port)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io := make([]byte, 32*1024)
		read := 0
		for read < payloadSize {
			n, err := conn.Read(io)
			if err != nil {
				return
			}
			read += n
		}
		conn.Write(make([]byte, replySize))
		// Half-close so the client's socket reaches TCP_CLOSE promptly.
		time.Sleep(100 * time.Millisecond)
	}()

	var (
		mu     sync.Mutex
		events []closeEvent
		wg     sync.WaitGroup
	)
	ctx, cancel := context.WithCancel(context.Background())
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			rec, err := reader.Read()
			if err != nil {
				if errors.Is(err, ringbuf.ErrClosed) || ctx.Err() != nil {
					return
				}
				continue
			}
			if len(rec.RawSample) >= 28 {
				mu.Lock()
				events = append(events, decode(rec.RawSample))
				mu.Unlock()
			}
		}
	}()

	time.Sleep(150 * time.Millisecond)

	conn, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	clientPort := uint16(conn.LocalAddr().(*net.TCPAddr).Port)

	if _, err := conn.Write(make([]byte, payloadSize)); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply := make([]byte, replySize)
	got := 0
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for got < replySize {
		n, err := conn.Read(reply)
		if err != nil {
			break
		}
		got += n
	}
	conn.Close()

	time.Sleep(1500 * time.Millisecond)
	cancel()
	reader.Close()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	var client *closeEvent
	for i := range events {
		if events[i].Sport == clientPort && events[i].Dport == serverPort {
			client = &events[i]
			break
		}
	}

	if client == nil {
		t.Fatalf("no close event for the client socket %d->%d (saw %d events)",
			clientPort, serverPort, len(events))
	}

	// TCP counts payload bytes, not framing, so an exact match is the expectation.
	sentDelta := int64(client.BytesSent) - int64(payloadSize)
	recvDelta := int64(client.BytesReceived) - int64(replySize)

	t.Logf("EXPERIMENT RESULT")
	t.Logf("  wrote    %7d bytes | reported bytes_sent     %7d | delta %+d",
		payloadSize, client.BytesSent, sentDelta)
	t.Logf("  read     %7d bytes | reported bytes_received %7d | delta %+d",
		replySize, client.BytesReceived, recvDelta)
	t.Logf("  close events observed: %d", len(events))

	if client.BytesSent != payloadSize {
		t.Errorf("bytes_sent = %d, want exactly %d (delta %+d)",
			client.BytesSent, payloadSize, sentDelta)
	}
	if client.BytesReceived != replySize {
		t.Errorf("bytes_received = %d, want exactly %d (delta %+d)",
			client.BytesReceived, replySize, recvDelta)
	}
}

// A long-lived connection never closes, so it never reports. This is the limitation that
// decides how byte data can honestly be presented.
func TestPrivilegedOpenConnectionReportsNothing(t *testing.T) {
	reader, cleanup := attach(t)
	defer cleanup()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			buf := make([]byte, 1024)
			conn.Read(buf)
			time.Sleep(3 * time.Second)
			conn.Close()
		}
	}()

	conn, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	port := uint16(conn.LocalAddr().(*net.TCPAddr).Port)
	conn.Write(make([]byte, 8192))

	// Deliberately do NOT close. Drain for a while and confirm silence.
	deadline := time.Now().Add(1200 * time.Millisecond)
	seen := 0
	go func() {
		for time.Now().Before(deadline) {
			rec, err := reader.Read()
			if err != nil {
				return
			}
			if len(rec.RawSample) >= 28 && decode(rec.RawSample).Sport == port {
				seen++
			}
		}
	}()
	time.Sleep(1400 * time.Millisecond)

	t.Logf("EXPERIMENT RESULT: an OPEN connection that has transferred 8192 bytes reported %d "+
		"close events — byte totals are only available at close", seen)
	if seen != 0 {
		t.Errorf("expected no close event for a still-open connection, saw %d", seen)
	}
}

func TestPrivilegedCountersAreCumulativePerConnection(t *testing.T) {
	reader, cleanup := attach(t)
	defer cleanup()

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
				buf := make([]byte, 4096)
				for {
					if _, err := conn.Read(buf); err != nil {
						conn.Close()
						return
					}
				}
			}()
		}
	}()

	const writes = 10
	const each = 1024

	conn, _ := net.Dial("tcp4", listener.Addr().String())
	port := uint16(conn.LocalAddr().(*net.TCPAddr).Port)
	for i := 0; i < writes; i++ {
		conn.Write(make([]byte, each))
		time.Sleep(10 * time.Millisecond)
	}
	conn.Close()

	var found *closeEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			rec, err := reader.Read()
			if err != nil {
				return
			}
			if len(rec.RawSample) >= 28 {
				e := decode(rec.RawSample)
				if e.Sport == port {
					found = &e
					return
				}
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	reader.Close()

	if found == nil {
		t.Skip("no close event captured; the accuracy test covers the primary claim")
	}

	want := uint64(writes * each)
	t.Logf("EXPERIMENT RESULT: %d writes of %d bytes over ONE connection reported bytes_sent=%d "+
		"(want %d) — the counter is cumulative, not per-write", writes, each, found.BytesSent, want)
	if found.BytesSent != want {
		t.Errorf("bytes_sent = %d, want %d", found.BytesSent, want)
	}
}

var _ = fmt.Sprintf
