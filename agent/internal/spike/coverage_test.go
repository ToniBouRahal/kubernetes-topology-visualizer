//go:build privileged

package spike

import (
	"testing"
	"time"

	"github.com/cilium/ebpf/link"
)

// The coverage measurement — the number the decision actually turns on (P4-X1).
//
// Accuracy is settled: bytes reported at close are exact. The open question is what FRACTION of
// connections close inside an observation window. Kubernetes workloads hold connections open
// (gRPC, connection pools, HTTP keep-alive), and those are usually the busiest edges. If most
// connections never close during a window, byte totals would systematically under-report exactly
// the traffic a topology view most needs to show.
//
// Measured against the live demo cluster, so the ratio reflects real workloads rather than a
// synthetic loop.
func TestPrivilegedWindowCoverage(t *testing.T) {
	const window = 60 * time.Second

	objs, tpClose, tpEstablish, cleanup := attachBoth(t)
	defer cleanup()
	_ = tpClose
	_ = tpEstablish

	start := time.Now()
	time.Sleep(window)
	elapsed := time.Since(start)

	established := readStat(t, objs, 0)
	closedWithBytes := readStat(t, objs, 1)
	bytesTotal := readStat(t, objs, 2)
	closedZero := readStat(t, objs, 3)
	unmatched := readStat(t, objs, 4)

	closedAll := closedWithBytes + closedZero

	t.Logf("EXPERIMENT RESULT — window coverage over %.0fs on the live cluster", elapsed.Seconds())
	t.Logf("  active opens observed (collector's filter) : %d", established)
	t.Logf("  connections closed in-window               : %d (%d carried bytes, %d carried none)",
		closedAll, closedWithBytes, closedZero)
	t.Logf("  bytes attributable in-window               : %d", bytesTotal)
	t.Logf("  closes discarded as not-an-active-open     : %d (server side of other connections)",
		unmatched)

	if established == 0 {
		t.Skip("no TCP activity observed; run with the demo workload applied")
	}

	coverage := float64(closedAll) / float64(established) * 100
	t.Logf("  CLOSE-TO-OPEN RATIO                        : %.1f%%", coverage)
	t.Logf("")
	t.Logf("  Reading: a ratio near 100%% means connections are short-lived and byte totals would")
	t.Logf("  land in roughly the right bucket. A ratio well below 100%% means long-lived")
	t.Logf("  connections dominate and their bytes stay invisible until they eventually close.")

	// Deliberately not an assertion on the ratio. The experiment records what the workload does;
	// the decision is made in docs/evaluation/byte-accounting.md, not by a threshold here.
}

func readStat(t *testing.T, objs *TcpBytesObjects, key uint32) uint64 {
	t.Helper()
	var v uint64
	if err := objs.SpikeStats.Lookup(&key, &v); err != nil {
		t.Fatalf("stat %d: %v", key, err)
	}
	return v
}

func attachBoth(t *testing.T) (*TcpBytesObjects, link.Link, link.Link, func()) {
	t.Helper()
	prepare(t)

	objs := &TcpBytesObjects{}
	if err := LoadTcpBytesObjects(objs, nil); err != nil {
		t.Skipf("cannot load spike objects (needs root): %v", err)
	}

	tpClose, err := link.Tracepoint("sock", "inet_sock_set_state", objs.TraceClose, nil)
	if err != nil {
		objs.Close()
		t.Fatalf("attach close: %v", err)
	}
	tpEstablish, err := link.Tracepoint("sock", "inet_sock_set_state", objs.TraceEstablish, nil)
	if err != nil {
		tpClose.Close()
		objs.Close()
		t.Fatalf("attach establish: %v", err)
	}

	return objs, tpClose, tpEstablish, func() {
		tpEstablish.Close()
		tpClose.Close()
		objs.Close()
	}
}
