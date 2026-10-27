package commands

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/auth"
)

const testToken = "test-token-never-printed"

var now = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeAPI is an httptest stand-in for the frontend: route picks the status and fixture for each
// request, and every request is recorded.
type fakeAPI struct {
	t     *testing.T
	srv   *httptest.Server
	route func(r *http.Request) (status int, file string)

	mu   sync.Mutex
	reqs []*http.Request
}

func newFakeAPI(t *testing.T, route func(r *http.Request) (int, string)) *fakeAPI {
	f := &fakeAPI{t: t, route: route}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Clone(context.Background()))
		f.mu.Unlock()
		status, file := f.route(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if file != "" {
			_, _ = w.Write(fixture(t, file))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

// fixtures serves the happy-path responses by endpoint.
func fixtures(r *http.Request) (int, string) {
	switch p := r.URL.Path; {
	case p == "/api/v1/namespaces":
		return 200, "namespaces.json"
	case p == "/api/v1/graph" && r.URL.Query().Get("query") == "backend":
		return 200, "graph.query-backend.json"
	case p == "/api/v1/graph" && r.URL.Query().Get("query") != "":
		return 200, "graph.empty.json"
	case p == "/api/v1/graph":
		return 200, "graph.json"
	case p == "/api/v1/nodes/k8s:kind-topology:demo:Deployment:backend":
		return 200, "node.deployment-backend.json"
	case p == "/api/v1/edges/k8s:kind-topology:demo:Deployment:client|k8s:kind-topology:demo:Service:backend|TCP|8080":
		return 200, "edge.json"
	case strings.HasPrefix(p, "/api/v1/edges/"):
		return 404, "edge.not-found.json"
	case p == "/api/v1/diff":
		return 200, "diff.json"
	case p == "/api/v1/anomalies":
		return 200, "anomalies.json"
	}
	return 500, ""
}

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

type failingToken struct{}

func (failingToken) Token(context.Context) (string, error) {
	return "", errors.New("the stored session expired and could not be refreshed")
}

type notSignedInToken struct{}

func (notSignedInToken) Token(context.Context) (string, error) {
	return "", &auth.NotSignedInError{Server: "http://127.0.0.1"}
}

type issuerDownToken struct{}

func (issuerDownToken) Token(context.Context) (string, error) {
	return "", &auth.ConnectionError{Target: "issuer http://127.0.0.1:9/dex", Err: errors.New("connection refused")}
}

type result struct {
	code           int
	stdout, stderr string
}

func run(t *testing.T, server string, tokens api.TokenSource, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	env := Env{
		Stdout: &out, Stderr: &errOut,
		Now: func() time.Time { return now },
		Client: func(_ context.Context, o ClientOptions) (*api.Client, error) {
			return api.New(o.Server, http.DefaultClient, tokens)
		},
	}
	code, ok := Run(context.Background(), env, args[0], append([]string{"--server", server}, args[1:]...))
	if !ok {
		t.Fatalf("%s is not a read command", args[0])
	}
	if strings.Contains(out.String()+errOut.String(), testToken) {
		t.Errorf("the token appeared in the output")
	}
	return result{code, out.String(), errOut.String()}
}

func lines(s string) []string { return strings.Split(strings.TrimSuffix(s, "\n"), "\n") }

// T-15.1: each read command prints the expected lines.
func TestReadCommandsPrintTheExpectedLines(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"namespaces"}, []string{
			"window 2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z", "data", "demo"}},
		{[]string{"graph"}, []string{
			"window 2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z",
			"3 relationships, 5 nodes, 177 connections",
			"Deployment/backend  →  EXTERNAL           TCP:443   (4)",
			"Deployment/backend  →  StatefulSet/redis  TCP:6379  (143)",
			"Deployment/client   →  Service/backend    TCP:8080  (30, 2 failed/aborted)"}},
		{[]string{"deps", "backend", "--kind", "deployment"}, []string{
			"Deployment/backend in namespace demo",
			"window 2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z",
			"Deployment/backend  →  EXTERNAL           TCP:443   (4)",
			"Deployment/backend  →  StatefulSet/redis  TCP:6379  (143)"}},
		{[]string{"diff", "--from", "2h", "--to", "now"}, []string{
			"baseline 2026-08-10T10:00:00Z to 2026-08-10T11:00:00Z",
			"current  2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z",
			"1 new, 1 removed, 2 changed, 0 unchanged",
			"NEW              k8s:kind-topology:demo:Deployment:backend  →  external:EXTERNAL                             TCP:443   (4)",
			"REMOVED          k8s:kind-topology:demo:Deployment:backend  →  k8s:kind-topology:data:StatefulSet:legacy-db  TCP:5432  (12)",
			"CHANGED 100→143  k8s:kind-topology:demo:Deployment:backend  →  k8s:kind-topology:data:StatefulSet:redis      TCP:6379",
			"CHANGED 20→30    k8s:kind-topology:demo:Deployment:client   →  k8s:kind-topology:demo:Service:backend        TCP:8080"}},
		{[]string{"edge", "k8s:kind-topology:demo:Deployment:client|k8s:kind-topology:demo:Service:backend|TCP|8080"}, []string{
			"window 2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z",
			"Deployment/client  →  Service/backend  TCP:8080  (30, 2 failed/aborted)",
			"first seen 2026-08-10T11:29:51Z, last seen 2026-08-10T11:30:00Z"}},
		{[]string{"anomalies", "--since", "1h"}, []string{
			"window 2026-08-10T11:00:00Z to 2026-08-10T12:00:00Z",
			"baseline 2026-08-09T11:00:00Z to 2026-08-10T11:00:00Z",
			"1 critical, 2 warning",
			"critical  CANARY_CONTACT  Deployment/backend   →  Deployment/legacy-admin  TCP:22",
			"    3 connection attempt(s) to a canary workload; nothing legitimate connects to one",
			"warning   NEW_EDGE        Deployment/backend   →  Deployment/legacy-admin  TCP:22",
			"    not observed in the previous 24 h",
			"warning   NEW_EDGE        Deployment/reporter  →  Deployment/payment       TCP:6380",
			"    not observed in the previous 24 h"}},
	} {
		r := run(t, fake.srv.URL, staticToken(testToken), tc.args...)
		if r.code != ExitOK || !slices.Equal(lines(r.stdout), tc.want) {
			t.Errorf("%v: exit %d, stderr %q\n got:\n%s\nwant:\n%s", tc.args, r.code, r.stderr, r.stdout, strings.Join(tc.want, "\n"))
		}
	}
	for _, req := range fake.requests() {
		if got := req.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("%s: Authorization = %q, want the bearer token", req.URL.Path, got)
		}
	}
}

// T-15.1: -o json prints the payload byte for byte.
func TestJSONOutputIsThePayloadUnchanged(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	for _, tc := range []struct {
		args    []string
		fixture string
	}{
		{[]string{"namespaces"}, "namespaces.json"},
		{[]string{"graph"}, "graph.json"},
		{[]string{"deps", "backend", "--kind", "Deployment"}, "node.deployment-backend.json"},
		{[]string{"diff", "--from", "2h", "--to", "now"}, "diff.json"},
		{[]string{"edge", "k8s:kind-topology:demo:Deployment:client|k8s:kind-topology:demo:Service:backend|TCP|8080"}, "edge.json"},
		{[]string{"anomalies"}, "anomalies.json"},
	} {
		r := run(t, fake.srv.URL, nil, append(tc.args, "-o", "json")...)
		if r.code != ExitOK || r.stdout != string(fixture(t, tc.fixture)) {
			t.Errorf("%v -o json: exit %d, stdout differs from %s", tc.args, r.code, tc.fixture)
		}
	}
}

// T-15.1: an empty result is success.
func TestEmptyResultExitsZero(t *testing.T) {
	fake := newFakeAPI(t, func(*http.Request) (int, string) { return 200, "graph.empty.json" })
	r := run(t, fake.srv.URL, nil, "graph", "--from", "2026-08-10T08:00:00Z", "--to", "2026-08-10T09:00:00Z")
	if r.code != ExitOK || !strings.Contains(r.stdout, "no relationships observed in this window") {
		t.Errorf("exit %d, stdout %q", r.code, r.stdout)
	}
}

// T-15.1 and D-15.4: 401 exits 2 naming login; other API errors 3 with the server's detail;
// an unreachable server 4; a credential that cannot be used 2.
func TestExitCodes(t *testing.T) {
	unauthorized := newFakeAPI(t, func(*http.Request) (int, string) { return 401, "" })
	r := run(t, unauthorized.srv.URL, staticToken(testToken), "graph")
	if r.code != ExitAuth || !strings.Contains(r.stderr, `kubectl topology login --server `+unauthorized.srv.URL) {
		t.Errorf("401: exit %d, stderr %q", r.code, r.stderr)
	}

	srv := newFakeAPI(t, fixtures)
	r = run(t, srv.srv.URL, nil, "edge", "no-such-edge")
	if r.code != ExitAPI || !strings.Contains(r.stderr, "no edge 'no-such-edge' observed in this window") {
		t.Errorf("404: exit %d, stderr %q", r.code, r.stderr)
	}

	unavailable := newFakeAPI(t, func(*http.Request) (int, string) { return 503, "" })
	if r = run(t, unavailable.srv.URL, nil, "namespaces"); r.code != ExitAPI {
		t.Errorf("503: exit %d, stderr %q", r.code, r.stderr)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	if r = run(t, closed.URL, nil, "namespaces"); r.code != ExitConnection {
		t.Errorf("connection refused: exit %d, stderr %q", r.code, r.stderr)
	}

	// A credential that cannot be used, for a reason signing in again would not fix.
	r = run(t, srv.srv.URL, failingToken{}, "namespaces")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "cannot use the stored sign-in") || strings.Contains(r.stderr, "login") {
		t.Errorf("token source failure: exit %d, stderr %q", r.code, r.stderr)
	}
	r = run(t, srv.srv.URL, notSignedInToken{}, "namespaces")
	if r.code != ExitAuth || !strings.Contains(r.stderr, "kubectl topology login --server") {
		t.Errorf("not signed in: exit %d, stderr %q", r.code, r.stderr)
	}
	r = run(t, srv.srv.URL, issuerDownToken{}, "namespaces")
	if r.code != ExitConnection || strings.Contains(r.stderr, "login") {
		t.Errorf("issuer unreachable: exit %d, stderr %q", r.code, r.stderr)
	}

	before := len(srv.requests())
	for _, args := range [][]string{
		{"graph", "--since", "1h", "--from", "2h", "--to", "now"},
		{"graph", "--kind", "ReplicaSet"},
		{"graph", "-o", "yaml"},
		{"deps"},
		{"edge"},
		{"diff", "--from", "2h"},
		{"namespaces", "--since", "soon"},
	} {
		if r = run(t, srv.srv.URL, nil, args...); r.code != ExitUsage {
			t.Errorf("%v: exit %d, want usage error", args, r.code)
		}
	}
	if n := len(srv.requests()) - before; n != 0 {
		t.Errorf("usage errors sent %d requests", n)
	}
}

// D-15.13: with no stored credential the request goes out without an Authorization header.
func TestNoTokenSendsNoAuthorization(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	run(t, fake.srv.URL, nil, "namespaces")
	if h := fake.requests()[0].Header.Values("Authorization"); len(h) != 0 {
		t.Errorf("Authorization = %q, want none", h)
	}
}

// T-15.2: an ambiguous name lists its candidates and exits 1, without reading any node.
func TestDepsAmbiguousListsCandidates(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	r := run(t, fake.srv.URL, nil, "deps", "backend")
	if r.code != ExitUsage {
		t.Fatalf("exit %d, want %d", r.code, ExitUsage)
	}
	for _, want := range []string{"matches 2 nodes", "--kind Deployment -n demo", "--kind Service -n demo"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr %q lacks %q", r.stderr, want)
		}
	}
	for _, req := range fake.requests() {
		if strings.HasPrefix(req.URL.Path, "/api/v1/nodes/") {
			t.Errorf("an ambiguous name still read %s", req.URL.Path)
		}
	}
}

// T-15.2: an absent name is "not observed in this window", success, and no node is read.
func TestDepsAbsentIsNotObserved(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	r := run(t, fake.srv.URL, nil, "deps", "payments", "--since", "1h")
	if r.code != ExitOK || !strings.Contains(r.stdout, "payments: not observed in this window") {
		t.Errorf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout, "not exist") {
		t.Errorf("absence worded as non-existence: %q", r.stdout)
	}
	if len(fake.requests()) != 1 {
		t.Errorf("%d requests, want only the graph", len(fake.requests()))
	}
}

// T-15.2: the resolved ID reaches /nodes/{id} verbatim — escaped for the path, never split —
// over exactly the window the graph answered for. Flags may follow the name.
func TestDepsPassesTheIDVerbatim(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	r := run(t, fake.srv.URL, nil, "deps", "backend", "-n", "demo", "--kind", "Deployment", "--since", "1h")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	reqs := fake.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want graph then node", len(reqs))
	}
	g, n := reqs[0], reqs[1]
	if g.URL.Path != "/api/v1/graph" || g.URL.Query().Get("query") != "backend" || g.URL.Query().Get("window") != "1h" {
		t.Errorf("graph request = %s", g.URL)
	}
	if n.URL.Path != "/api/v1/nodes/k8s:kind-topology:demo:Deployment:backend" {
		t.Errorf("node path = %q", n.URL.Path)
	}
	if q := n.URL.Query(); q.Get("from") != "2026-08-10T11:00:00Z" || q.Get("to") != "2026-08-10T12:00:00Z" || q.Has("window") {
		t.Errorf("node window = %s, want the graph's window as from/to", n.URL.RawQuery)
	}
}

func TestEdgeIDIsPathEscapedNotParsed(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	id := "k8s:kind-topology:demo:Deployment:client|k8s:kind-topology:demo:Service:backend|TCP|8080"
	if r := run(t, fake.srv.URL, nil, "edge", id); r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	req := fake.requests()[0]
	if want := "/api/v1/edges/" + url.PathEscape(id); req.URL.EscapedPath() != want || req.URL.Path != "/api/v1/edges/"+id {
		t.Errorf("edge path = %q (raw %q)", req.URL.Path, req.URL.EscapedPath())
	}
}

// T-15.3 at the command level: presets go as window, others as RFC 3339 UTC, and diff's two
// periods reach the API adjacent and equal.
func TestTimeFlagsReachTheAPI(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	run(t, fake.srv.URL, nil, "graph", "--since", "15m", "-n", "demo", "-n", "data", "--no-external")
	run(t, fake.srv.URL, nil, "graph", "--since", "2h")
	run(t, fake.srv.URL, nil, "diff", "--from", "2h", "--to", "now", "-n", "demo")
	r := run(t, fake.srv.URL, nil, "diff", "--from", "2h", "--to", "now", "--length", "90m")
	if r.code != ExitUsage || !strings.Contains(r.stderr, "overlap") {
		t.Errorf("overlapping diff: exit %d, stderr %q", r.code, r.stderr)
	}

	reqs := fake.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3 (the overlapping diff must not be sent)", len(reqs))
	}
	q := reqs[0].URL.Query()
	if q.Get("window") != "15m" || q.Has("from") || !slices.Equal(q["namespace"], []string{"demo", "data"}) ||
		q.Get("include_external") != "false" {
		t.Errorf("preset graph query = %s", reqs[0].URL.RawQuery)
	}
	q = reqs[1].URL.Query()
	if q.Has("window") || q.Get("from") != "2026-08-10T10:00:00Z" || q.Get("to") != "2026-08-10T12:00:00Z" {
		t.Errorf("non-preset graph query = %s", reqs[1].URL.RawQuery)
	}
	q = reqs[2].URL.Query()
	for k, want := range map[string]string{
		"baseline_from": "2026-08-10T10:00:00Z", "baseline_to": "2026-08-10T11:00:00Z",
		"current_from": "2026-08-10T11:00:00Z", "current_to": "2026-08-10T12:00:00Z", "namespace": "demo",
	} {
		if q.Get(k) != want {
			t.Errorf("diff %s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestUnknownCommandIsNotHandled(t *testing.T) {
	if _, ok := Run(context.Background(), Env{}, "login", nil); ok {
		t.Error("login is not a read command")
	}
}

// D-15.4: a refresh that cannot reach the issuer is a connection failure (exit 4), not a
// sign-out: the stored tokens are kept and no login is suggested. Driven through a real
// auth.Source, so the error's path from the refresh to the exit code is the one main wires.
func TestRefreshWithIssuerDownExitsConnection(t *testing.T) {
	srv := newFakeAPI(t, fixtures)
	issuer := httptest.NewServer(http.NotFoundHandler())
	issuer.Close()
	server, err := auth.NormalizeServer(srv.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := &auth.Store{Dir: filepath.Join(t.TempDir(), "kubectl-topology"), LockTimeout: 2 * time.Second}
	stored := &auth.Entry{Issuer: issuer.URL + "/dex", ClientID: "topology-cli", Scopes: []string{"openid"},
		IDToken: testToken, Expiry: time.Now().Add(-time.Minute), RefreshToken: "refresh-" + testToken}
	if err := store.Update(context.Background(), func(f *auth.File) error {
		f.Servers[server] = stored
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	r := run(t, srv.srv.URL, &auth.Source{Store: store, Server: server}, "namespaces")
	if r.code != ExitConnection || strings.Contains(r.stderr, "login") || !strings.Contains(r.stderr, "port-forward") {
		t.Errorf("exit %d, stderr %q; want exit 4 with no login hint", r.code, r.stderr)
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("%d requests reached the API with an expired token", n)
	}
	f, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if e := f.Servers[server]; e.IDToken != stored.IDToken || e.RefreshToken != stored.RefreshToken {
		t.Errorf("the tokens were not kept after a refresh that could not be sent")
	}
}

// ADR-016 D-16.7: graph -o mermaid prints the diagram of the response it was given.
func TestGraphMermaidOutput(t *testing.T) {
	fake := newFakeAPI(t, func(*http.Request) (int, string) { return 200, "graph.export.json" })
	r := run(t, fake.srv.URL, nil, "graph", "-o", "mermaid")
	if r.code != ExitOK || r.stdout != string(fixture(t, "graph.export.mmd")) {
		t.Errorf("exit %d, stderr %q, stdout:\n%s", r.code, r.stderr, r.stdout)
	}
}

// Only graph draws a diagram; asking another command for one is a usage error, not text.
func TestMermaidIsRefusedOutsideGraph(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	r := run(t, fake.srv.URL, nil, "anomalies", "-o", "mermaid")
	if r.code != ExitUsage || !strings.Contains(r.stderr, `want text or json`) {
		t.Errorf("exit %d, stderr %q", r.code, r.stderr)
	}
	if n := len(fake.requests()); n != 0 {
		t.Errorf("%d requests sent for a usage error", n)
	}
}

// The anomalies request carries the graph's window and filters.
func TestAnomaliesSendsTheGraphFilters(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	r := run(t, fake.srv.URL, nil, "anomalies", "--since", "15m", "-n", "demo", "--kind", "deployment", "--no-external")
	if r.code != ExitOK {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	q := fake.requests()[0].URL.Query()
	if q.Get("window") != "15m" || q.Get("namespace") != "demo" || q.Get("kind") != "Deployment" || q.Get("include_external") != "false" {
		t.Errorf("query = %v", q)
	}
}
