//go:build privileged

package spike

import (
	"net"
	"sync"
	"testing"
	"time"
)

// The counter-case to TestPrivilegedWindowCoverage.
//
// The demo workload makes short HTTP calls, so nearly every connection closes inside the window
// and coverage looks excellent. Kubernetes also runs on connections that do NOT close: database
// pools, gRPC channels, HTTP keep-alive. Those are typically the highest-volume edges.
//
// This measures that case explicitly, because a decision resting on the demo workload alone would
// generalise a number that does not generalise.
func TestPrivilegedPersistentConnectionCoverage(t *testing.T) {
	const conns = 8
	const window = 20 * time.Second
	const chunk = 4096

	objs, _, _, cleanup := attachBoth(t)
	defer cleanup()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, chunk)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()

	baseEstablished := readStat(t, objs, 0)
	baseClosed := readStat(t, objs, 1)
	baseBytes := readStat(t, objs, 2)

	var sentByUs uint64
	var sentMu sync.Mutex

	// Open persistent connections and keep them transferring for the whole window without ever
	// closing them — the shape of a connection pool.
	for i := 0; i < conns; i++ {
		conn, err := net.Dial("tcp4", listener.Addr().String())
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		defer conn.Close()

		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			payload := make([]byte, chunk)
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := c.Write(payload); err != nil {
					return
				}
				sentMu.Lock()
				sentByUs += chunk
				sentMu.Unlock()
				time.Sleep(20 * time.Millisecond)
			}
		}(conn)
	}

	time.Sleep(window)
	close(stop)
	wg.Wait()

	established := readStat(t, objs, 0) - baseEstablished
	closed := readStat(t, objs, 1) - baseClosed
	reported := readStat(t, objs, 2) - baseBytes

	sentMu.Lock()
	actuallySent := sentByUs
	sentMu.Unlock()

	t.Logf("EXPERIMENT RESULT — %d PERSISTENT connections over %.0fs", conns, window.Seconds())
	t.Logf("  bytes actually transferred by the test : %d", actuallySent)
	t.Logf("  active opens observed                  : %d", established)
	t.Logf("  connections closed in-window           : %d", closed)
	t.Logf("  bytes reported in-window               : %d", reported)

	if actuallySent == 0 {
		t.Fatal("test transferred nothing; the measurement would be meaningless")
	}

	// The test's own traffic is the interesting part, but the host is busy, so `reported` also
	// contains unrelated short-lived connections closing. The claim is therefore the weaker,
	// unambiguous one: the persistent connections did not report.
	visible := float64(reported) / float64(actuallySent) * 100
	_ = closed
	t.Logf("  reported/transferred                   : %.1f%% (includes unrelated host traffic,"+
		" so the true figure for these connections is lower still)", visible)

	// Asserting on the close COUNT would be wrong: that counter is host-wide, and a busy cluster
	// closes hundreds of unrelated connections during the window. The unambiguous claim is the
	// byte gap — whatever else closed, this test's 32 MB is not in the total.
	if reported >= actuallySent/10 {
		t.Errorf("reported %d bytes against %d transferred (%.1f%%); the persistent connections' "+
			"bytes were expected to be absent", reported, actuallySent, visible)
	}
	t.Logf("")
	t.Logf("  CONCLUSION: %d connections carried %d bytes and contributed nothing to the window's",
		conns, actuallySent)
	t.Logf("  byte totals, because bytes are only readable at close.")
}
