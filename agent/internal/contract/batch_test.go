package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// examplesDir resolves contracts/examples/ relative to this test file.
func examplesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "contracts", "examples"))
	if err != nil {
		t.Fatalf("resolve examples dir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("contracts/examples not found at %s: %v", dir, err)
	}
	return dir
}

type manifestCase struct {
	Fixture        string `json:"fixture"`
	ExpectedStatus int    `json:"expected_status"`
	Rule           string `json:"rule"`
}

func loadManifest(t *testing.T) []manifestCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(examplesDir(t), "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m struct {
		Cases []manifestCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if len(m.Cases) == 0 {
		t.Fatal("manifest declares no cases")
	}
	return m.Cases
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(examplesDir(t), name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// TestRoundTripValidFixtures is T-3.3 and the cheapest cross-language drift detector we have.
//
// It unmarshals each valid fixture into the Go structs and marshals it back, then compares the
// two documents structurally. A renamed JSON tag, a dropped field, or a changed type on either
// side of the language boundary fails here rather than at the demo.
func TestRoundTripValidFixtures(t *testing.T) {
	for _, c := range loadManifest(t) {
		if c.ExpectedStatus != 202 {
			continue
		}
		t.Run(c.Fixture, func(t *testing.T) {
			original := readFixture(t, c.Fixture)

			var batch IngestBatch
			if err := json.Unmarshal(original, &batch); err != nil {
				t.Fatalf("unmarshal into Go structs: %v", err)
			}

			reencoded, err := json.Marshal(&batch)
			if err != nil {
				t.Fatalf("re-marshal: %v", err)
			}

			var want, got map[string]any
			if err := json.Unmarshal(original, &want); err != nil {
				t.Fatalf("parse original: %v", err)
			}
			if err := json.Unmarshal(reencoded, &got); err != nil {
				t.Fatalf("parse re-encoded: %v", err)
			}

			if !reflect.DeepEqual(want, got) {
				t.Errorf("round trip is not field-equivalent.\n original: %s\nre-encoded: %s",
					original, reencoded)
			}
		})
	}
}

// TestValidateAcceptsValidFixtures: agent-side validation must not reject what the backend accepts.
func TestValidateAcceptsValidFixtures(t *testing.T) {
	for _, c := range loadManifest(t) {
		if c.ExpectedStatus != 202 {
			continue
		}
		t.Run(c.Fixture, func(t *testing.T) {
			var batch IngestBatch
			if err := json.Unmarshal(readFixture(t, c.Fixture), &batch); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if err := batch.Validate(); err != nil {
				t.Errorf("valid fixture rejected by agent-side validation: %v", err)
			}
		})
	}
}

// TestValidateRejectsInvalidFixtures: the agent must never spend a delivery attempt on a batch
// the backend is certain to reject.
//
// The extra-field case is exempt: Pydantic's extra="forbid" rejects unknown keys, while Go's
// encoding/json ignores them by default. That asymmetry is real and documented rather than
// papered over — the agent constructs batches from typed structs, so it cannot originate one.
func TestValidateRejectsInvalidFixtures(t *testing.T) {
	for _, c := range loadManifest(t) {
		if c.ExpectedStatus == 202 {
			continue
		}
		if strings.Contains(c.Fixture, "extra-field") {
			t.Run(c.Fixture+" (known Go/Pydantic asymmetry)", func(t *testing.T) {
				t.Skip("encoding/json ignores unknown fields; the agent cannot emit this shape")
			})
			continue
		}
		t.Run(c.Fixture, func(t *testing.T) {
			var batch IngestBatch
			if err := json.Unmarshal(readFixture(t, c.Fixture), &batch); err != nil {
				// A type-level mismatch is itself a valid rejection.
				return
			}
			if err := batch.Validate(); err == nil {
				t.Errorf("invalid fixture accepted by agent-side validation (%s): %s",
					c.Rule, c.Fixture)
			}
		})
	}
}

// TestExternalNodeRoundTripsNullNamespace guards the pointer choice on NodeRef.Namespace.
// With a plain string, null would silently become "" and the external node would gain a namespace.
func TestExternalNodeRoundTripsNullNamespace(t *testing.T) {
	var batch IngestBatch
	if err := json.Unmarshal(readFixture(t, "batch.valid.json"), &batch); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var found bool
	for _, e := range batch.Edges {
		if e.Target.ID == ExternalNodeID {
			found = true
			if e.Target.Namespace != nil {
				t.Errorf("external node namespace should be nil, got %q", *e.Target.Namespace)
			}
		}
	}
	if !found {
		t.Fatal("batch.valid.json must contain an edge to the external node")
	}
}

// TestBytesAbsentNotZero: omitempty on a *int64 keeps "not measured" distinct from "measured zero".
func TestBytesAbsentNotZero(t *testing.T) {
	var batch IngestBatch
	if err := json.Unmarshal(readFixture(t, "batch.valid-minimal.json"), &batch); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if batch.Edges[0].BytesSent != nil {
		t.Error("bytes_sent must be nil when the field is absent")
	}
	out, err := json.Marshal(&batch)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "bytes_sent") {
		t.Errorf("unmeasured bytes must not be emitted: %s", out)
	}
}

func TestBuildNodeIDGrammar(t *testing.T) {
	got, err := BuildNodeID("kind-topology", "demo", "Deployment", "client")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "k8s:kind-topology:demo:Deployment:client"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildNodeIDRejectsAmbiguousSegments(t *testing.T) {
	cases := []struct {
		name                              string
		cluster, namespace, kind, resName string
	}{
		{"empty name", "c", "demo", "Deployment", ""},
		{"empty namespace", "c", "", "Deployment", "client"},
		{"colon in name", "c", "demo", "Deployment", "has:colon"},
		{"colon in cluster", "has:colon", "demo", "Deployment", "client"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildNodeID(tc.cluster, tc.namespace, tc.kind, tc.resName); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

// TestAllowedKindsAreExactlySix mirrors the Python assertion. ReplicaSet must never be present.
func TestAllowedKindsAreExactlySix(t *testing.T) {
	want := map[string]bool{
		"Service": true, "Deployment": true, "StatefulSet": true,
		"DaemonSet": true, "Job": true, "Pod": true,
	}
	if !reflect.DeepEqual(AllowedKinds, want) {
		t.Errorf("AllowedKinds drifted: got %v, want %v", AllowedKinds, want)
	}
	if AllowedKinds["ReplicaSet"] {
		t.Error("ReplicaSet must not be an allowed kind: pods collapse through it to the Deployment")
	}
}
