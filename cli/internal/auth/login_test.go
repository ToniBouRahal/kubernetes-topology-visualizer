package auth

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// T-15.4 — login against a fake OIDC provider.

func testStore(t *testing.T) *Store {
	t.Helper()
	return &Store{Dir: filepath.Join(t.TempDir(), "kubectl-topology"), LockTimeout: 2 * time.Second}
}

// requireFreePorts skips when something outside the test already holds a callback port: the
// tests assert on the real fixed ports, not stand-ins.
func requireFreePorts(t *testing.T) {
	t.Helper()
	for _, p := range CallbackPorts {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			t.Skipf("callback port %d is in use outside the test: %v", p, err)
		}
		_ = ln.Close()
	}
}

func occupy(t *testing.T, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("occupying %d: %v", port, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
}

func signIn(t *testing.T, f *fakeOIDC, store *Store, o SignInOptions) (*SignInResult, error) {
	t.Helper()
	o.Server = f.srv.URL
	if o.OpenBrowser == nil {
		o.OpenBrowser = browser(t)
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	return SignIn(context.Background(), store, o)
}

func TestFixedPortsAndTimeout(t *testing.T) {
	if !slices.Equal(CallbackPorts, []int{38085, 38086, 38087}) {
		t.Fatalf("CallbackPorts = %v, want the three registered redirect ports", CallbackPorts)
	}
	if CallbackTimeout != 5*time.Minute {
		t.Fatalf("CallbackTimeout = %s, want 5m", CallbackTimeout)
	}
}

func TestSignInPKCES256AndStores(t *testing.T) {
	requireFreePorts(t)
	f := newFakeOIDC(t)
	store := testStore(t)
	var prompt bytes.Buffer
	res, err := signIn(t, f, store, SignInOptions{Prompt: &prompt})
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if res.Email != "admin@example.com" || res.NoAuth {
		t.Fatalf("result = %+v", res)
	}

	q := f.authQueries[0]
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("authorisation request lacks an S256 challenge: %v", q)
	}
	if q.Get("client_id") != "topology-cli" || q.Get("response_type") != "code" || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatalf("authorisation request = %v", q)
	}
	if got := q.Get("redirect_uri"); got != "http://127.0.0.1:38085/callback" {
		t.Fatalf("redirect_uri = %q, want the first fixed port on 127.0.0.1", got)
	}
	// The fake redeems the code only with the verifier matching the challenge, so a stored entry
	// is proof the verifier was sent.
	file, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	e := file.Servers[f.srv.URL]
	if !e.SignedIn() || e.RefreshToken == "" || e.Issuer != f.issuer() || e.ClientID != "topology-cli" {
		t.Fatalf("stored entry = %+v", e)
	}
	if time.Until(e.Expiry) < 59*time.Minute {
		t.Fatalf("expiry %s is not the ID token's exp", e.Expiry)
	}
	if !strings.Contains(prompt.String(), "/dex/auth?") {
		t.Fatalf("the URL was not printed for a browser-less user: %q", prompt.String())
	}
	assertNoSecrets(t, f, prompt.String())
}

func TestCallbackListenerIsLoopbackOnly(t *testing.T) {
	requireFreePorts(t)
	ln, err := listenLoopback(CallbackPorts)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)
	if !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listener bound to %s, want 127.0.0.1 only", addr)
	}
	// Nothing else on the machine's other addresses answers on that port.
	ifaces, _ := net.InterfaceAddrs()
	for _, a := range ifaces {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.To4() == nil {
			continue
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ipn.IP.String(), strconv.Itoa(addr.Port)), time.Second)
		if err == nil {
			_ = c.Close()
			t.Fatalf("callback port reachable on %s", ipn.IP)
		}
	}
}

func TestSignInUsesFirstFreePort(t *testing.T) {
	requireFreePorts(t)
	occupy(t, 38085)
	f := newFakeOIDC(t)
	if _, err := signIn(t, f, testStore(t), SignInOptions{}); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if got := f.authQueries[0].Get("redirect_uri"); got != "http://127.0.0.1:38086/callback" {
		t.Fatalf("redirect_uri = %q, want the second port when the first is busy", got)
	}
}

func TestSignInAllPortsBusy(t *testing.T) {
	requireFreePorts(t)
	for _, p := range CallbackPorts {
		occupy(t, p)
	}
	f := newFakeOIDC(t)
	_, err := signIn(t, f, testStore(t), SignInOptions{})
	if err == nil {
		t.Fatal("SignIn succeeded with every callback port busy")
	}
	for _, p := range CallbackPorts {
		if !strings.Contains(err.Error(), "127.0.0.1:"+strconv.Itoa(p)) {
			t.Fatalf("error %q does not name port %d", err, p)
		}
	}
	if len(f.authQueries) != 0 {
		t.Fatal("the browser was sent to the provider without a listener")
	}
}

func TestWrongStateRefusedAndListenerClosedAfterOneCallback(t *testing.T) {
	requireFreePorts(t)
	f := newFakeOIDC(t)
	forgedStatus := make(chan int, 1)
	open := func(u string) error {
		go func() {
			parsed, _ := url.Parse(u)
			cb := parsed.Query().Get("redirect_uri")
			// A forged callback first: right path, wrong state.
			resp, err := http.Get(cb + "?code=forged&state=not-the-state")
			if err != nil {
				t.Errorf("forged callback: %v", err)
				forgedStatus <- 0
				return
			}
			_ = resp.Body.Close()
			forgedStatus <- resp.StatusCode
			_ = browser(t)(u)
		}()
		return nil
	}
	res, err := signIn(t, f, testStore(t), SignInOptions{OpenBrowser: open})
	if err != nil {
		t.Fatalf("SignIn after a forged callback: %v", err)
	}
	if res.Email == "" {
		t.Fatal("no sign-in result")
	}
	if got := <-forgedStatus; got != http.StatusBadRequest {
		t.Fatalf("wrong-state callback answered %d, want 400", got)
	}
	// One callback ends it: the port no longer accepts connections.
	c, err := net.DialTimeout("tcp", "127.0.0.1:38085", time.Second)
	if err == nil {
		_ = c.Close()
		t.Fatal("callback listener still open after the sign-in completed")
	}
}

func TestCallbackTimeout(t *testing.T) {
	requireFreePorts(t)
	f := newFakeOIDC(t)
	start := time.Now()
	_, err := signIn(t, f, testStore(t), SignInOptions{Timeout: 150 * time.Millisecond,
		OpenBrowser: func(string) error { return nil }})
	if err == nil || !strings.Contains(err.Error(), "gave up waiting") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout not honoured")
	}
	if c, err := net.DialTimeout("tcp", "127.0.0.1:38085", time.Second); err == nil {
		_ = c.Close()
		t.Fatal("listener left open after the timeout")
	}
}

func TestGroupsScopeOnlyWhenSupported(t *testing.T) {
	requireFreePorts(t)
	for _, tc := range []struct {
		supported []string
		want      string
	}{
		{[]string{"openid", "email", "profile", "offline_access"}, "openid email profile offline_access"},
		{[]string{"openid", "email", "groups", "profile", "offline_access"}, "openid email profile offline_access groups"},
		{nil, "openid email profile offline_access"},
	} {
		f := newFakeOIDC(t)
		f.scopesSupported = tc.supported
		if _, err := signIn(t, f, testStore(t), SignInOptions{}); err != nil {
			t.Fatalf("SignIn: %v", err)
		}
		if got := f.authQueries[0].Get("scope"); got != tc.want {
			t.Fatalf("scopes_supported %v: scope = %q, want %q", tc.supported, got, tc.want)
		}
	}
}

func TestScopesOverride(t *testing.T) {
	requireFreePorts(t)
	f := newFakeOIDC(t)
	f.scopesSupported = append(f.scopesSupported, "groups")
	store := testStore(t)
	if _, err := signIn(t, f, store, SignInOptions{Scopes: []string{"openid", "email"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.authQueries[0].Get("scope"); got != "openid email" {
		t.Fatalf("scope = %q", got)
	}
}

func TestAuthOffStoresNothing(t *testing.T) {
	f := newFakeOIDC(t)
	f.authOff = true
	store := testStore(t)
	res, err := signIn(t, f, store, SignInOptions{})
	if err != nil || !res.NoAuth {
		t.Fatalf("res = %+v, err = %v; want NoAuth", res, err)
	}
	if len(f.authQueries) != 0 {
		t.Fatal("the browser was sent to sign in to an auth-off server")
	}
	file, err := store.Load()
	if err != nil || len(file.Servers) != 0 {
		t.Fatalf("stored %v (err %v), want nothing", file, err)
	}
}

func TestLoginCommandOutput(t *testing.T) {
	requireFreePorts(t)
	f := newFakeOIDC(t)
	var out, errOut bytes.Buffer
	c := &Commands{Stdout: &out, Stderr: &errOut, Store: testStore(t), OpenBrowser: browser(t), Now: time.Now,
		CallbackTimeout: 10 * time.Second}
	if code := c.Login(context.Background(), []string{"--server", f.srv.URL}); code != 0 {
		t.Fatalf("login exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Signed in to "+f.srv.URL+" as admin@example.com") {
		t.Fatalf("stdout = %q", out.String())
	}
	assertNoSecrets(t, f, out.String(), errOut.String())

	f.authOff = true
	out.Reset()
	if code := c.Login(context.Background(), []string{"--server", f.srv.URL + "/"}); code != 0 {
		t.Fatalf("auth-off login exit %d", code)
	}
	if !strings.Contains(out.String(), "does not require sign-in") {
		t.Fatalf("stdout = %q", out.String())
	}
}

// D-15.4: a malformed flag is a usage error (exit 1), caught before anything is sent or bound.
func TestLoginUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--server", "notaurl"},
		{"--server", "http://localhost:8080?x=1"},
		{"--issuer", "ftp://issuer.example"},
		{"--ca-file", filepath.Join(t.TempDir(), "missing.pem")},
	} {
		var out, errOut bytes.Buffer
		c := &Commands{Stdout: &out, Stderr: &errOut, Store: testStore(t), Now: time.Now,
			OpenBrowser: func(string) error { t.Errorf("%v opened a browser", args); return nil }}
		if code := c.Login(context.Background(), args); code != exitUsage {
			t.Errorf("%v: exit %d, want %d; stderr %q", args, code, exitUsage, errOut.String())
		}
	}
}
