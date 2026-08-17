// Package delivery ships aggregated batches to the backend.
//
// The design exists to satisfy one requirement: a transient backend outage must not crash the
// agent, must not lose committed observations silently, and must not double-count on recovery
// (ADR-001 §6, ADR-002 D-2.6).
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/contract"
)

// Retry pacing. Jitter matters more than the exact bounds: without it, every agent in the
// cluster retries in lockstep and hits a recovering backend simultaneously.
const (
	baseBackoff = 500 * time.Millisecond
	maxBackoff  = 30 * time.Second
	httpTimeout = 10 * time.Second
)

// Counters expose delivery health. Without them a backend outage and an absence of traffic look
// identical from outside the agent.
type Counters struct {
	Sent      uint64 // accepted by the backend, including duplicates it recognised
	Duplicate uint64 // backend answered 200: already stored
	Retried   uint64
	Dropped   uint64 // evicted by queue overflow — the only lossy path, and it is counted
	Rejected  uint64 // permanently refused; retrying could never help
	QueueHigh uint64 // high-water mark, for sizing AGENT_MAX_PENDING_BATCHES
}

// Client owns the pending queue and the delivery loop.
type Client struct {
	url        string
	httpClient *http.Client
	log        *slog.Logger

	mu      sync.Mutex
	pending []contract.IngestBatch
	maxSize int
	signal  chan struct{}

	countersMu sync.Mutex
	counters   Counters

	// Injected so tests do not sleep for real.
	sleep func(context.Context, time.Duration) bool
	rand  *rand.Rand
}

func New(url string, maxPending int, log *slog.Logger) *Client {
	if maxPending < 1 {
		maxPending = 1
	}
	return &Client{
		url:        url,
		httpClient: &http.Client{Timeout: httpTimeout},
		log:        log,
		pending:    make([]contract.IngestBatch, 0, maxPending),
		maxSize:    maxPending,
		signal:     make(chan struct{}, 1),
		sleep:      sleepCtx,
		rand:       rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // pacing only
	}
}

// Enqueue accepts a batch for delivery.
//
// On overflow the OLDEST batch is evicted, not the newest. Recent topology is more useful than
// stale topology, and during a long outage the alternative — dropping what just arrived — would
// leave the graph frozen at the moment the outage began (ADR-002 D-2.6).
func (c *Client) Enqueue(batch contract.IngestBatch) {
	c.mu.Lock()
	if len(c.pending) >= c.maxSize {
		evicted := c.pending[0]
		c.pending = c.pending[1:]
		c.mu.Unlock()

		c.bump(func(k *Counters) { k.Dropped++ })
		c.log.Warn("delivery queue full; dropped the oldest batch",
			"batch_id", evicted.BatchID,
			"queue_size", c.maxSize,
			"hint", "raise AGENT_MAX_PENDING_BATCHES or investigate the backend")
		c.mu.Lock()
	}

	c.pending = append(c.pending, batch)
	depth := uint64(len(c.pending))
	c.mu.Unlock()

	c.bump(func(k *Counters) {
		if depth > k.QueueHigh {
			k.QueueHigh = depth
		}
	})

	// Non-blocking wake: the loop re-reads the queue anyway, so a missed signal costs nothing.
	select {
	case c.signal <- struct{}{}:
	default:
	}
}

// Run delivers batches until ctx is cancelled, then drains what remains within drainTimeout.
//
// It never returns an error for a delivery failure — an unreachable backend is an expected
// condition, not a reason to terminate the agent.
func (c *Client) Run(ctx context.Context, drainTimeout time.Duration) {
	for {
		batch, ok := c.peek()
		if !ok {
			select {
			case <-ctx.Done():
				c.drain(drainTimeout)
				return
			case <-c.signal:
				continue
			case <-time.After(time.Second):
				continue
			}
		}

		if c.attemptWithRetry(ctx, batch) {
			c.pop(batch.BatchID)
			continue
		}

		// attemptWithRetry only gives up when the context ends.
		if ctx.Err() != nil {
			c.drain(drainTimeout)
			return
		}
	}
}

// attemptWithRetry retries until the batch is settled or ctx ends. Returns true if the batch
// should leave the queue.
func (c *Client) attemptWithRetry(ctx context.Context, batch contract.IngestBatch) bool {
	backoff := baseBackoff

	for attempt := 0; ; attempt++ {
		outcome, err := c.post(ctx, batch)

		switch outcome {
		case outcomeAccepted:
			c.bump(func(k *Counters) { k.Sent++ })
			return true

		case outcomeDuplicate:
			// 200 means the backend already has it. That is success: the batch must leave the
			// queue, or the agent retries forever something already stored.
			c.bump(func(k *Counters) { k.Sent++; k.Duplicate++ })
			c.log.Debug("batch already ingested", "batch_id", batch.BatchID)
			return true

		case outcomePermanent:
			// Retrying cannot help. Drop it and make the reason visible rather than looping.
			c.bump(func(k *Counters) { k.Rejected++ })
			c.log.Error("backend permanently rejected the batch",
				"batch_id", batch.BatchID, "error", err,
				"hint", "the agent and backend contracts disagree; check schema_version")
			return true

		case outcomeRetryable:
			c.bump(func(k *Counters) { k.Retried++ })
			c.log.Warn("delivery failed; will retry",
				"batch_id", batch.BatchID, "attempt", attempt+1, "error", err,
				"backoff", backoff.String())
		}

		if !c.sleep(ctx, jitter(c.rand, backoff)) {
			return false // context ended mid-wait
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

type outcome int

const (
	outcomeAccepted outcome = iota
	outcomeDuplicate
	outcomePermanent
	outcomeRetryable
)

func (c *Client) post(ctx context.Context, batch contract.IngestBatch) (outcome, error) {
	body, err := json.Marshal(&batch)
	if err != nil {
		// A batch we cannot even encode will never encode. Dropping beats an infinite loop.
		return outcomePermanent, fmt.Errorf("marshal batch: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return outcomePermanent, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return outcomeRetryable, fmt.Errorf("post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
		return outcomeDuplicate, nil
	case resp.StatusCode == http.StatusAccepted:
		return outcomeAccepted, nil
	case resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode >= 500:
		return outcomeRetryable, fmt.Errorf("backend returned %s", resp.Status)
	case resp.StatusCode >= 400:
		// 400/413/422: the contract disagrees. No amount of retrying fixes that.
		return outcomePermanent, fmt.Errorf("backend returned %s", resp.Status)
	default:
		return outcomeRetryable, fmt.Errorf("unexpected status %s", resp.Status)
	}
}

// drain makes a bounded best effort to deliver what is queued at shutdown, so a rolling restart
// does not discard an interval that was already collected.
func (c *Client) drain(timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		batch, ok := c.peek()
		if !ok {
			return
		}
		if ctx.Err() != nil {
			c.mu.Lock()
			remaining := len(c.pending)
			c.mu.Unlock()
			if remaining > 0 {
				c.log.Warn("shutdown drain timed out with batches still queued",
					"remaining", remaining)
			}
			return
		}
		// One attempt each during shutdown: retrying with backoff would exhaust the deadline on
		// the first batch and lose the rest.
		if out, err := c.post(ctx, batch); out == outcomeAccepted || out == outcomeDuplicate {
			c.bump(func(k *Counters) { k.Sent++ })
			c.pop(batch.BatchID)
		} else {
			c.log.Warn("could not deliver during shutdown", "batch_id", batch.BatchID, "error", err)
			return
		}
	}
}

func (c *Client) peek() (contract.IngestBatch, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return contract.IngestBatch{}, false
	}
	return c.pending[0], true
}

// pop removes a batch by id rather than by position: Enqueue may have evicted the head while a
// delivery was in flight, and removing by position would then discard the wrong batch.
func (c *Client) pop(batchID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, b := range c.pending {
		if b.BatchID == batchID {
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return
		}
	}
}

func (c *Client) QueueDepth() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

func (c *Client) Counters() Counters {
	c.countersMu.Lock()
	defer c.countersMu.Unlock()
	return c.counters
}

func (c *Client) bump(f func(*Counters)) {
	c.countersMu.Lock()
	defer c.countersMu.Unlock()
	f(&c.counters)
}

// jitter spreads retries so a fleet of agents does not stampede a recovering backend. Full
// jitter — a uniform draw over [0, d) — rather than a fixed fraction.
func jitter(r *rand.Rand, d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(r.Int63n(int64(d)))
}

// sleepCtx waits, returning false if the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
