// Command agent is the per-node topology collector.
//
// Pipeline: capture → resolve → aggregate → emit (source of truth §7).
// Phase 1 emits normalized batches to structured logs; Phase 2 adds delivery to the backend.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/aggregate"
	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/collector"
	"github.com/fyp/kubernetes-topology-visualizer/agent/internal/resolver"
)

type config struct {
	clusterID       string
	nodeName        string
	flushInterval   time.Duration
	infraPorts      []uint16
	debugRawEvents  bool
	healthPort      int
	metricsPort     int
	backendIngest   string
	intervalSeconds int
}

func loadConfig() (config, error) {
	c := config{
		clusterID:      env("CLUSTER_ID", "kind-topology"),
		nodeName:       env("NODE_NAME", ""),
		debugRawEvents: env("AGENT_DEBUG_RAW_EVENTS", "false") == "true",
		backendIngest:  env("BACKEND_INGEST_URL", ""),
	}

	if c.nodeName == "" {
		host, err := os.Hostname()
		if err != nil {
			return c, fmt.Errorf("NODE_NAME is unset and the hostname is unavailable: %w", err)
		}
		c.nodeName = host
	}

	seconds, err := strconv.Atoi(env("AGENT_FLUSH_INTERVAL_SECONDS", "10"))
	if err != nil || seconds < 1 {
		return c, fmt.Errorf("AGENT_FLUSH_INTERVAL_SECONDS must be a positive integer, got %q",
			env("AGENT_FLUSH_INTERVAL_SECONDS", ""))
	}
	c.intervalSeconds = seconds
	c.flushInterval = time.Duration(seconds) * time.Second

	if c.healthPort, err = strconv.Atoi(env("AGENT_HEALTH_PORT", "8081")); err != nil {
		return c, fmt.Errorf("AGENT_HEALTH_PORT: %w", err)
	}
	if c.metricsPort, err = strconv.Atoi(env("AGENT_METRICS_PORT", "9090")); err != nil {
		return c, fmt.Errorf("AGENT_METRICS_PORT: %w", err)
	}

	c.infraPorts, err = parsePorts(env("INFRASTRUCTURE_PORTS", ""))
	if err != nil {
		return c, err
	}

	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parsePorts(raw string) ([]uint16, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ports []uint16
	for _, field := range strings.Split(raw, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		n, err := strconv.ParseUint(field, 10, 16)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("INFRASTRUCTURE_PORTS contains an invalid port %q", field)
		}
		ports = append(ports, uint16(n))
	}
	return ports, nil
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("agent exited with an error", "error", err)
		os.Exit(1)
	}
	log.Info("agent stopped cleanly")
}

func run(log *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	agentID := "topology-agent/" + cfg.nodeName
	log = log.With("agent_id", agentID, "cluster_id", cfg.clusterID, "node", cfg.nodeName)
	log.Info("starting",
		"flush_interval_seconds", cfg.intervalSeconds,
		"infrastructure_ports", len(cfg.infraPorts),
		"debug_raw_events", cfg.debugRawEvents)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var ready atomic.Bool
	agg := aggregate.New(cfg.clusterID, agentID, cfg.infraPorts)

	// Health and metrics come up first so the kubelet sees a live process while the informer
	// caches sync, which can take a few seconds on a busy cluster.
	var collectorRef atomic.Pointer[collector.Collector]
	healthSrv := startHealthServer(cfg, &ready, agg, &collectorRef, log)
	defer shutdown(healthSrv, log)

	// ── Kubernetes metadata ─────────────────────────────────────────────────────────────
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster config (the agent must run as a pod with a ServiceAccount): %w", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("build Kubernetes client: %w", err)
	}

	caches, err := resolver.NewInformerCaches(client)
	if err != nil {
		return fmt.Errorf("build informers: %w", err)
	}

	// Sync before attaching the BPF program, so the first events already have metadata to
	// resolve against instead of producing a burst of unresolved endpoints (ADR-002 D-2.3).
	log.Info("waiting for informer caches to sync")
	syncStart := time.Now()
	if err := caches.Start(ctx); err != nil {
		return fmt.Errorf("sync informer caches: %w", err)
	}
	log.Info("informer caches synced", "duration_ms", time.Since(syncStart).Milliseconds())

	res := resolver.New(cfg.clusterID, caches)

	// ── eBPF ────────────────────────────────────────────────────────────────────────────
	coll, err := collector.New()
	if err != nil {
		return fmt.Errorf("start eBPF collector: %w", err)
	}
	defer func() {
		if err := coll.Close(); err != nil {
			log.Warn("collector close", "error", err)
		}
	}()
	collectorRef.Store(coll)
	log.Info("eBPF program attached", "tracepoint", "sock/inet_sock_set_state")

	ready.Store(true)

	// ── Flush loop ──────────────────────────────────────────────────────────────────────
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(cfg.flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// Final flush so a shutdown does not discard a partial interval.
				emitBatch(log, agg, cfg.intervalSeconds, "shutdown")
				return
			case <-ticker.C:
				emitBatch(log, agg, cfg.intervalSeconds, "interval")
			}
		}
	}()

	err = coll.Run(ctx, func(ev collector.Event) {
		// Raw addresses are logged only behind an explicit debug flag. ADR-001 §6 requires
		// raw event logging to be off by default so external IPs never reach the log by
		// accident.
		if cfg.debugRawEvents {
			log.Debug("raw event",
				"src", ev.SrcIP.String(), "src_port", ev.SrcPort,
				"dst", ev.DstIP.String(), "dst_port", ev.DstPort, "pid", ev.PID)
		}

		agg.Add(aggregate.Observation{
			Source:          res.ResolveSource(ev.SrcIP),
			Target:          res.ResolveDestination(ev.DstIP, ev.DstPort),
			Protocol:        "TCP",
			DestinationPort: ev.DstPort,
			Timestamp:       ev.Timestamp,
		})
	})

	<-done
	return err
}

// emitBatch is the Phase 1 sink: normalized batches to structured logs. Phase 2 (P2-A17)
// replaces this with the bounded, retrying delivery queue.
func emitBatch(log *slog.Logger, agg *aggregate.Aggregator, intervalSeconds int, trigger string) {
	batch, ok := agg.Flush(intervalSeconds)
	if !ok {
		log.Debug("no edges observed in interval", "trigger", trigger)
		return
	}

	if err := batch.Validate(); err != nil {
		// The agent must never spend a delivery attempt on a batch the backend will reject.
		log.Error("flushed batch failed contract validation", "error", err, "batch_id", batch.BatchID)
		return
	}

	encoded, err := json.Marshal(&batch)
	if err != nil {
		log.Error("marshal batch", "error", err, "batch_id", batch.BatchID)
		return
	}

	log.Info("batch",
		"trigger", trigger,
		"batch_id", batch.BatchID,
		"edges", len(batch.Edges),
		"payload", json.RawMessage(encoded))
}

func startHealthServer(
	cfg config,
	ready *atomic.Bool,
	agg *aggregate.Aggregator,
	coll *atomic.Pointer[collector.Collector],
	log *slog.Logger,
) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	// Ready only once the informer caches have synced and the BPF program is attached.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready: informer sync or BPF attach pending\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		c := agg.Counters()

		var kernel collector.Stats
		if active := coll.Load(); active != nil {
			if s, err := active.Stats(); err == nil {
				kernel = s
			}
		}

		metrics := []struct {
			name, help, typ string
			value           uint64
		}{
			{"topology_agent_raw_events_received_total", "TCP active opens submitted by the BPF program", "counter", kernel.EventsSubmitted},
			{"topology_agent_kernel_samples_lost_total", "Events dropped because the ring buffer was full", "counter", kernel.RingbufDropped},
			{"topology_agent_events_filtered_family_total", "Events discarded: not AF_INET", "counter", kernel.FilteredFamily},
			{"topology_agent_events_filtered_protocol_total", "Events discarded: not TCP", "counter", kernel.FilteredProtocol},
			{"topology_agent_events_filtered_transition_total", "Events discarded: not an active open", "counter", kernel.FilteredTransition},
			{"topology_agent_observations_total", "Resolved observations offered to the aggregator", "counter", c.Observed},
			{"topology_agent_filtered_infrastructure_port_total", "Observations dropped on an infrastructure port", "counter", c.FilteredInfraPort},
			{"topology_agent_endpoints_unresolved_total", "Observations dropped because an endpoint could not be resolved", "counter", c.UnresolvedEndpoints},
			{"topology_agent_filtered_not_graphable_total", "Observations dropped as host or non-graph traffic", "counter", c.FilteredNotGraphable},
			{"topology_agent_edges_flushed_total", "Aggregated edges emitted", "counter", c.AggregatedEdgesFlushed},
			{"topology_agent_batches_flushed_total", "Batches emitted", "counter", c.BatchesFlushed},
		}

		for _, m := range metrics {
			_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %d\n", m.name, m.help, m.name, m.typ, m.name, m.value)
		}
		_, _ = fmt.Fprintf(w, "# HELP topology_agent_pending_edges Edges accumulated in the current interval\n"+
			"# TYPE topology_agent_pending_edges gauge\ntopology_agent_pending_edges %d\n", agg.PendingEdges())
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.healthPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("health server", "error", err)
		}
	}()

	return srv
}

func shutdown(srv *http.Server, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn("health server shutdown", "error", err)
	}
}
