// Configuration defaults that carry a privacy guarantee — ADR-001 §6, test T-2.13.
//
// §6 requires that raw event logging is OFF unless explicitly enabled: with it on, source and
// destination addresses — including external IPs the system must never persist — are written to
// the pod log. That default was correct in the code but nothing asserted it, so a one-character
// edit could have inverted it silently. It is the kind of requirement that is only ever noticed
// after it has been broken.
package main

import (
	"testing"
)

func TestRawEventLoggingIsOffUnlessExplicitlyEnabled(t *testing.T) {
	t.Setenv("BACKEND_INGEST_URL", "http://backend:8000/api/v1/ingest/batches")
	t.Setenv("NODE_NAME", "node-a")

	cases := []struct {
		name    string
		value   string
		set     bool
		enabled bool
	}{
		{"unset", "", false, false},
		{"empty", "", true, false},
		{"false", "false", true, false},
		{"true", "true", true, true},
		// Anything that is not exactly "true" leaves it off. A privacy default should not be
		// switched on by a typo, a "1", or a capitalised "True" that someone assumed would work.
		{"1", "1", true, false},
		{"True", "True", true, false},
		{"TRUE", "TRUE", true, false},
		{"yes", "yes", true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("AGENT_DEBUG_RAW_EVENTS", tc.value)
			} else {
				t.Setenv("AGENT_DEBUG_RAW_EVENTS", "")
			}

			cfg, err := loadConfig()
			if err != nil {
				t.Fatalf("loadConfig: %v", err)
			}
			if cfg.debugRawEvents != tc.enabled {
				t.Errorf("AGENT_DEBUG_RAW_EVENTS=%q gave debugRawEvents=%v, want %v — raw address "+
					"logging must stay off unless the value is exactly \"true\" (ADR-001 §6)",
					tc.value, cfg.debugRawEvents, tc.enabled)
			}
		})
	}
}

// The ingest URL has no sensible default: guessing one would make an agent that silently delivers
// nowhere, which looks identical to a cluster with no traffic.
func TestIngestURLIsRequired(t *testing.T) {
	t.Setenv("NODE_NAME", "node-a")
	t.Setenv("BACKEND_INGEST_URL", "")

	if _, err := loadConfig(); err == nil {
		t.Error("loadConfig accepted an empty BACKEND_INGEST_URL; an agent that delivers nowhere " +
			"is indistinguishable from a cluster with no traffic")
	}
}
