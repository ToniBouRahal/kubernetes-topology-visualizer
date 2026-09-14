package collector

import (
	"encoding/binary"
	"testing"
)

func TestDecodeConnectionOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name             string
		failed, measured bool
		micros           uint64
	}{
		{"successful measured", false, true, 1234}, {"successful submicrosecond", false, true, 0},
		{"successful untracked", false, false, 0}, {"failed setup", true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := make([]byte, 40)
			raw[offVersion] = EventSchemaVersion
			if tc.failed {
				raw[27] = 1
			}
			if tc.measured {
				raw[28] = 1
			}
			binary.LittleEndian.PutUint64(raw[32:], tc.micros)
			event, err := (&Collector{}).decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			if event.Failed != tc.failed {
				t.Fatalf("failed=%v", event.Failed)
			}
			if (event.ConnectLatencyUS != nil) != tc.measured {
				t.Fatalf("latency=%v", event.ConnectLatencyUS)
			}
			if tc.measured && *event.ConnectLatencyUS != int64(tc.micros) {
				t.Fatalf("latency=%d", *event.ConnectLatencyUS)
			}
		})
	}
}

func TestDecodeRejectsInvalidOutcomeTiming(t *testing.T) {
	for _, tc := range []struct {
		name           string
		outcome, known byte
		duration       uint64
	}{
		{"unknown outcome", 2, 0, 0}, {"unknown timing flag", 0, 2, 0},
		{"failure with success timing", 1, 1, 20}, {"unmeasured with duration", 0, 0, 10},
		{"duration over signed range", 0, 1, 1 << 63},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := make([]byte, eventSize)
			raw[offVersion] = EventSchemaVersion
			raw[offOutcome] = tc.outcome
			raw[offDurationKnown] = tc.known
			binary.LittleEndian.PutUint64(raw[offDurationUs:], tc.duration)
			if _, err := (&Collector{}).decode(raw); err == nil {
				t.Fatal("invalid outcome accepted")
			}
		})
	}
}
