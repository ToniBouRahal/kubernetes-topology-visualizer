package delivery

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/contract"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestClient replaces the sleep with an immediate return so retry paths run at full speed.
// Backoff *pacing* is asserted separately in TestJitterStaysWithinBound.
func newTestClient(t *testing.T, url string, maxPending int) *Client {
	t.Helper()
	c := New(url, maxPending, quietLogger())
	c.sleep = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	return c
}

func batch(id string) contract.IngestBatch {
	ns := "demo"
	return contract.IngestBatch{
		SchemaVersion:   contract.SchemaVersion,
		ClusterID:       "kind-topology",
		AgentID:         "topology-agent/test",
		BatchID:         id,
		ObservedAt:      time.Now().UTC(),
		IntervalSeconds: 10,
		Edges: []contract.EdgeObservation{{
			Source:          contract.NodeRef{ID: "k8s:c:demo:Deployment:a", Kind: "Deployment", Namespace: &ns, Name: "a"},
			Target:          contract.NodeRef{ID: "k8s:c:demo:Service:b", Kind: "Service", Namespace: &ns, Name: "b"},
			Protocol:        contract.ProtocolTCP,
			DestinationPort: 8080,
			ConnectionCount: 1,
			FirstSeen:       time.Now().UTC(),
			LastSeen:        time.Now().UTC(),
		}},
	}
}

// 26-char Crockford base32 ids, so the batches are contract-valid.
func id(n int) string {
	base := []byte("01J8ZQ9X7K4M2N6P8R3T5V7W9Y")
	base[len(base)-1] = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"[n%32]
	return string(base)
}

func runUntilEmpty(t *testing.T, c *Client, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); c.Run(ctx, time.Second) }()

	deadline := time.Now().Add(timeout)
	for c.QueueDepth() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	wg.Wait()
}

// Success: 202 settles the batch and empties the queue.
func TestSuccessfulDelivery(t *testing.T) {
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&received, 1)
		var decoded contract.IngestBatch
		if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
			t.Errorf("backend could not decode the batch: %v", err)
		}
		if err := decoded.Validate(); err != nil {
			t.Errorf("agent sent a batch its own contract rejects: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 8)
	c.Enqueue(batch(id(1)))
	runUntilEmpty(t, c, 2*time.Second)

	if got := atomic.LoadInt32(&received); got != 1 {
		t.Errorf("backend received %d batches, want 1", got)
	}
	if c.QueueDepth() != 0 {
		t.Error("a delivered batch must leave the queue")
	}
	if c.Counters().Sent != 1 {
		t.Errorf("Sent = %d, want 1", c.Counters().Sent)
	}
}

// 200 means the backend already stored it. Treating that as failure would retry forever.
func TestDuplicateResponseIsSuccessNotRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 8)
	c.Enqueue(batch(id(2)))
	runUntilEmpty(t, c, 2*time.Second)

	if c.QueueDepth() != 0 {
		t.Fatal("a 200 must settle the batch; the agent would otherwise retry something already stored")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("backend called %d times, want 1 — no retry should follow a 200", got)
	}
	k := c.Counters()
	if k.Duplicate != 1 || k.Sent != 1 {
		t.Errorf("Duplicate=%d Sent=%d, want 1 and 1", k.Duplicate, k.Sent)
	}
}

// A transient outage must be survived, not fatal — and must not lose the batch.
func TestRetriesUntilBackendRecovers(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) < 4 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 8)
	c.Enqueue(batch(id(3)))
	runUntilEmpty(t, c, 5*time.Second)

	if c.QueueDepth() != 0 {
		t.Error("the batch should have been delivered once the backend recovered")
	}
	if got := atomic.LoadInt32(&calls); got < 4 {
		t.Errorf("backend called %d times, want at least 4 (3 failures then success)", got)
	}
	if c.Counters().Retried < 3 {
		t.Errorf("Retried = %d, want at least 3", c.Counters().Retried)
	}
	if c.Counters().Sent != 1 {
		t.Errorf("Sent = %d, want exactly 1 — a retry must not count as an extra delivery",
			c.Counters().Sent)
	}
}

// 4xx other than 408/429 is permanent: the contracts disagree and retrying cannot fix that.
func TestPermanentRejectionIsDroppedNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&calls, 1)
				w.WriteHeader(code)
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL, 8)
			c.Enqueue(batch(id(4)))
			runUntilEmpty(t, c, 2*time.Second)

			if c.QueueDepth() != 0 {
				t.Error("a permanently rejected batch must leave the queue")
			}
			if got := atomic.LoadInt32(&calls); got != 1 {
				t.Errorf("backend called %d times, want 1 — no retry for a permanent rejection", got)
			}
			if c.Counters().Rejected != 1 {
				t.Errorf("Rejected = %d, want 1", c.Counters().Rejected)
			}
		})
	}
}

// 429 and 5xx are retryable; the distinction from a permanent 4xx is load-bearing.
func TestRetryableStatuses(t *testing.T) {
	for _, code := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if atomic.AddInt32(&calls, 1) < 2 {
					w.WriteHeader(code)
					return
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL, 8)
			c.Enqueue(batch(id(5)))
			runUntilEmpty(t, c, 3*time.Second)

			if c.Counters().Retried < 1 {
				t.Errorf("%d should have been retried", code)
			}
			if c.Counters().Sent != 1 {
				t.Errorf("Sent = %d, want 1", c.Counters().Sent)
			}
		})
	}
}

// Overflow evicts the OLDEST batch. During a long outage, keeping the newest means the graph
// resumes at the present rather than frozen at the moment the outage began.
func TestQueueOverflowDropsOldest(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1", 3) // unreachable on purpose

	for i := 1; i <= 5; i++ {
		c.Enqueue(batch(id(i)))
	}

	if c.QueueDepth() != 3 {
		t.Fatalf("QueueDepth = %d, want the bound of 3", c.QueueDepth())
	}
	if c.Counters().Dropped != 2 {
		t.Errorf("Dropped = %d, want 2", c.Counters().Dropped)
	}

	c.mu.Lock()
	kept := []string{c.pending[0].BatchID, c.pending[1].BatchID, c.pending[2].BatchID}
	c.mu.Unlock()

	want := []string{id(3), id(4), id(5)}
	for i := range want {
		if kept[i] != want[i] {
			t.Errorf("queue[%d] = %s, want %s — the OLDEST batches should have been evicted",
				i, kept[i], want[i])
		}
	}
}

// The high-water mark is what makes AGENT_MAX_PENDING_BATCHES sizable from evidence.
func TestQueueHighWaterMarkIsRecorded(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1", 10)
	for i := 1; i <= 4; i++ {
		c.Enqueue(batch(id(i)))
	}
	if c.Counters().QueueHigh != 4 {
		t.Errorf("QueueHigh = %d, want 4", c.Counters().QueueHigh)
	}
}

// Shutdown must flush what is queued, so a rolling restart does not discard collected data.
func TestShutdownDrainsQueue(t *testing.T) {
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&received, 1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, 8)
	ctx, cancel := context.WithCancel(context.Background())

	for i := 1; i <= 3; i++ {
		c.Enqueue(batch(id(i)))
	}

	// Cancel immediately: the drain, not the main loop, must deliver these.
	cancel()
	c.Run(ctx, 2*time.Second)

	if got := atomic.LoadInt32(&received); got != 3 {
		t.Errorf("backend received %d batches during drain, want 3", got)
	}
	if c.QueueDepth() != 0 {
		t.Errorf("QueueDepth = %d after drain, want 0", c.QueueDepth())
	}
}

// An unreachable backend must never terminate the agent.
func TestUnreachableBackendDoesNotBlockShutdown(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1", 4)
	c.Enqueue(batch(id(1)))

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() { c.Run(ctx, 100*time.Millisecond); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context ended; an unreachable backend must not hang shutdown")
	}
}

// Full jitter: a uniform draw over [0, d). Without it a fleet of agents retries in lockstep and
// stampedes a recovering backend.
func TestJitterStaysWithinBound(t *testing.T) {
	c := New("http://example.invalid", 1, quietLogger())
	const d = time.Second

	var sawBelowHalf bool
	for i := 0; i < 200; i++ {
		got := jitter(c.rand, d)
		if got < 0 || got >= d {
			t.Fatalf("jitter returned %v, outside [0, %v)", got, d)
		}
		if got < d/2 {
			sawBelowHalf = true
		}
	}
	if !sawBelowHalf {
		t.Error("jitter never fell below half the backoff; it does not look uniform")
	}
	if got := jitter(c.rand, 0); got != 0 {
		t.Errorf("jitter(0) = %v, want 0", got)
	}
}

// pop removes by id, not position: Enqueue may evict the head while a delivery is in flight,
// and removing by position would then discard the wrong batch.
func TestPopRemovesByIdentityNotPosition(t *testing.T) {
	c := newTestClient(t, "http://127.0.0.1:1", 4)
	c.Enqueue(batch(id(1)))
	c.Enqueue(batch(id(2)))
	c.Enqueue(batch(id(3)))

	c.pop(id(2))

	c.mu.Lock()
	remaining := []string{c.pending[0].BatchID, c.pending[1].BatchID}
	c.mu.Unlock()

	if remaining[0] != id(1) || remaining[1] != id(3) {
		t.Errorf("after popping the middle batch the queue is %v, want [%s %s]",
			remaining, id(1), id(3))
	}
}
