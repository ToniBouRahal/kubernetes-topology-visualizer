package aggregate

import (
	"testing"
	"time"
)

func TestOutcomeCountsAndLatencySamplesStaySeparate(t *testing.T) {
	a := newAgg(t)
	base := obs(workload("app", "Deployment", "client"), service("app", "server"), 8080, time.Now().UTC())
	fast, slow := int64(100), int64(900)
	for _, latency := range []*int64{&fast, &slow, nil} {
		observation := base
		observation.ConnectLatencyUS = latency
		a.Add(observation)
	}
	failure := base
	failure.Failed = true
	failure.ConnectLatencyUS = &slow
	a.Add(failure)
	batch, ok := a.Flush(10)
	if !ok {
		t.Fatal("missing batch")
	}
	edge := batch.Edges[0]
	if edge.ConnectionCount != 3 || edge.FailedConnectionCount == nil || *edge.FailedConnectionCount != 1 {
		t.Fatalf("incorrect outcomes: %+v", edge)
	}
	if edge.ConnectLatencyCount != 2 || edge.ConnectLatencySumUS != 1000 {
		t.Fatalf("incorrect successful samples: %+v", edge)
	}
	if err := batch.Validate(); err != nil {
		t.Fatal(err)
	}
	a.Add(failure)
	batch, _ = a.Flush(10)
	if batch.Edges[0].ConnectionCount != 0 || *batch.Edges[0].FailedConnectionCount != 1 {
		t.Fatal("failure-only edge lost")
	}
	if err := batch.Validate(); err != nil {
		t.Fatal(err)
	}
}
