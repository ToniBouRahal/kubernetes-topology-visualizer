package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// T-15.5 — credential store, refresh under the lock, compare-before-clear, no cached tokens.

func seed(t *testing.T, store *Store, f *fakeOIDC, expiry time.Time) *Entry {
	t.Helper()
	f.mu.Lock()
	f.serial++
	id := makeJWT(map[string]any{"iss": f.issuer(), "aud": "topology-cli", "exp": expiry.Unix(), "email": f.email})
	f.refreshToken = "seed-refresh-secret"
	f.issued = append(f.issued, id, f.refreshToken)
	f.mu.Unlock()
	e := &Entry{Issuer: f.issuer(), ClientID: "topology-cli", Scopes: []string{"openid"}, IDToken: id,
		Expiry: expiry.UTC().Truncate(time.Second), RefreshToken: "seed-refresh-secret"}
	if err := store.Update(context.Background(), func(file *File) error {
		file.Servers[f.srv.URL] = e
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestStoreCreatesOwnerOnlyAndReplacesAtomically(t *testing.T) {
	store := testStore(t)
	syscallUmask(t, 0o022)
	if err := store.Update(context.Background(), func(f *File) error {
		f.Servers["http://localhost:8080"] = &Entry{Issuer: "http://localhost:8080/dex", IDToken: "x"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mode := func(p string) os.FileMode {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode().Perm()
	}
	if m := mode(store.Dir); m != 0o700 {
		t.Fatalf("dir mode %04o, want 0700", m)
	}
	if m := mode(store.path()); m != 0o600 {
		t.Fatalf("file mode %04o, want 0600", m)
	}
	if m := mode(filepath.Join(store.Dir, lockFile)); m != 0o600 {
		t.Fatalf("lock mode %04o, want 0600", m)
	}

	before, _ := os.ReadFile(store.path())
	boom := errors.New("boom")
	if err := store.Update(context.Background(), func(f *File) error {
		f.Servers["http://localhost:8080"].IDToken = "half-written"
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(store.path())
	if !bytes.Equal(before, after) {
		t.Fatal("a failed update changed the file")
	}
	// The replacement goes through a temporary file that does not survive.
	entries, _ := os.ReadDir(store.Dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
	inode := func() uint64 { return statIno(t, store.path()) }
	old := inode()
	if err := store.Update(context.Background(), func(f *File) error {
		f.Servers["http://other:1"] = &Entry{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if inode() == old {
		t.Fatal("the file was rewritten in place, not replaced by rename")
	}
}

func TestStoreRefusesLoosePermissions(t *testing.T) {
	store := testStore(t)
	if err := store.Update(context.Background(), func(*File) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.path(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := store.Load()
	if err == nil || !strings.Contains(err.Error(), "chmod 600 "+store.path()) {
		t.Fatalf("Load of a 0644 file: err = %v, want a refusal saying how to fix it", err)
	}
	src := &Source{Store: store, Server: "http://localhost:8080"}
	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("Token read a 0644 credentials file")
	}
	if err := os.Chmod(store.path(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "chmod 700") {
		t.Fatalf("Load in a 0755 directory: err = %v", err)
	}
}

func TestLockTimeout(t *testing.T) {
	store := testStore(t)
	store.LockTimeout = 150 * time.Millisecond
	if err := store.Update(context.Background(), func(*File) error { return nil }); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockExclusive(context.Background(), filepath.Join(store.Dir, lockFile), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	err = store.Update(context.Background(), func(*File) error { t.Fatal("ran without the lock"); return nil })
	if err == nil || !strings.Contains(err.Error(), "gave up") {
		t.Fatalf("err = %v, want a lock timeout", err)
	}
}

func TestDefaultLockTimeoutIs30s(t *testing.T) {
	if DefaultLockTimeout != 30*time.Second {
		t.Fatalf("DefaultLockTimeout = %s", DefaultLockTimeout)
	}
}

func TestTokenRefreshesWithin60s(t *testing.T) {
	f := newFakeOIDC(t)
	store := testStore(t)
	now := time.Now()
	src := &Source{Store: store, Server: f.srv.URL, Now: func() time.Time { return now }}

	seeded := seed(t, store, f, now.Add(2*time.Minute))
	tok, err := src.Token(context.Background())
	if err != nil || tok != seeded.IDToken || f.refreshes != 0 {
		t.Fatalf("token 2m from expiry: refreshed=%d err=%v; want the stored token, no refresh", f.refreshes, err)
	}

	seeded = seed(t, store, f, now.Add(30*time.Second))
	tok, err = src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok == seeded.IDToken || f.refreshes != 1 {
		t.Fatalf("token 30s from expiry: refreshes=%d, want 1 and a new token", f.refreshes)
	}
	file, _ := store.Load()
	e := file.Servers[f.srv.URL]
	if e.IDToken != tok || e.RefreshToken == "seed-refresh-secret" {
		t.Fatal("the refreshed (rotated) tokens were not written back")
	}
}

func TestConcurrentRefreshersRefreshOnce(t *testing.T) {
	f := newFakeOIDC(t)
	f.refreshDelay = 200 * time.Millisecond
	dir := testStore(t).Dir
	first := &Store{Dir: dir, LockTimeout: 5 * time.Second}
	seed(t, first, f, time.Now().Add(10*time.Second))

	var wg sync.WaitGroup
	tokens := make([]string, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Separate Store values: each opens its own lock file description, as two
			// processes would.
			src := &Source{Store: &Store{Dir: dir, LockTimeout: 5 * time.Second}, Server: f.srv.URL}
			tokens[i], errs[i] = src.Token(context.Background())
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
	}
	if f.refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1 (the second must re-read under the lock and skip)", f.refreshes)
	}
	if tokens[0] != tokens[1] {
		t.Fatal("the two callers got different tokens")
	}
}

func TestFailedRefreshClearsTokens(t *testing.T) {
	f := newFakeOIDC(t)
	f.refuseRefresh = true
	store := testStore(t)
	seed(t, store, f, time.Now().Add(-time.Minute))
	src := &Source{Store: store, Server: f.srv.URL}
	_, err := src.Token(context.Background())
	var nse *NotSignedInError
	if !errors.As(err, &nse) {
		t.Fatalf("err = %v, want NotSignedInError", err)
	}
	want := `not signed in to ` + f.srv.URL + `: run "kubectl topology login --server ` + f.srv.URL + `"`
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err, want)
	}
	file, _ := store.Load()
	e := file.Servers[f.srv.URL]
	if e.SignedIn() || e.RefreshToken != "" || e.Issuer == "" {
		t.Fatalf("entry after a refused refresh = %+v; want tokens cleared, configuration kept", e)
	}
	assertNoSecrets(t, f, err.Error(), nse.Reason)
}

func TestFailedRefreshKeepsATokenRotatedMeanwhile(t *testing.T) {
	f := newFakeOIDC(t)
	f.refuseRefresh = true
	store := testStore(t)
	seed(t, store, f, time.Now().Add(-time.Minute))
	rotated := makeJWT(map[string]any{"iss": f.issuer(), "aud": "topology-cli", "exp": time.Now().Add(time.Hour).Unix()})
	// While the refused refresh is in flight, the file gains a newer refresh token (written by a
	// process that did not take the lock, the worst case the re-read guards against).
	f.onRefresh = func() {
		file, err := store.read()
		if err != nil {
			t.Error(err)
			return
		}
		e := file.Servers[f.srv.URL]
		e.IDToken, e.RefreshToken, e.Expiry = rotated, "rotated-refresh-secret", time.Now().Add(time.Hour)
		if err := store.write(file); err != nil {
			t.Error(err)
		}
	}
	tok, err := (&Source{Store: store, Server: f.srv.URL}).Token(context.Background())
	if err != nil || tok != rotated {
		t.Fatalf("Token = %v; want the token rotated in meanwhile, not a sign-out", err)
	}
	file, _ := store.Load()
	if file.Servers[f.srv.URL].RefreshToken != "rotated-refresh-secret" {
		t.Fatal("a refresh token that was not the failed one was cleared")
	}
}

func TestClearIfRefreshTokenIs(t *testing.T) {
	f := &File{Servers: map[string]*Entry{"s": {IDToken: "id", RefreshToken: "new"}}}
	if clearIfRefreshTokenIs(f, "s", "old") || !f.Servers["s"].SignedIn() {
		t.Fatal("cleared an entry whose refresh token had changed")
	}
	if !clearIfRefreshTokenIs(f, "s", "new") || f.Servers["s"].SignedIn() {
		t.Fatal("did not clear the entry holding the failed refresh token")
	}
}

func TestRefreshAfter401(t *testing.T) {
	f := newFakeOIDC(t)
	store := testStore(t)
	seeded := seed(t, store, f, time.Now().Add(time.Hour))
	src := &Source{Store: store, Server: f.srv.URL}

	// Another process already replaced the rejected token: no refresh, the stored one is used.
	tok, err := src.Refresh(context.Background(), "some-older-token")
	if err != nil || tok != seeded.IDToken || f.refreshes != 0 {
		t.Fatalf("Refresh(stale) = %v, refreshes %d", err, f.refreshes)
	}
	tok, err = src.Refresh(context.Background(), seeded.IDToken)
	if err != nil || tok == seeded.IDToken || f.refreshes != 1 {
		t.Fatalf("Refresh(current) = %v, refreshes %d; want one refresh and a new token", err, f.refreshes)
	}

	empty := &Source{Store: testStore(t), Server: f.srv.URL}
	if tok, err := empty.Token(context.Background()); tok != "" || err != nil {
		t.Fatalf("no entry: Token = %q, %v; want no token, no error (D-15.13)", tok, err)
	}
	var nse *NotSignedInError
	if _, err := empty.Refresh(context.Background(), ""); !errors.As(err, &nse) {
		t.Fatalf("no entry after 401: err = %v, want NotSignedInError", err)
	}
}

func TestSourceRereadsTheFileOnEveryCall(t *testing.T) {
	f := newFakeOIDC(t)
	store := testStore(t)
	first := seed(t, store, f, time.Now().Add(time.Hour))
	src := &Source{Store: store, Server: f.srv.URL}
	if tok, _ := src.Token(context.Background()); tok != first.IDToken {
		t.Fatal("first token")
	}
	second := seed(t, store, f, time.Now().Add(2*time.Hour))
	if tok, _ := src.Token(context.Background()); tok != second.IDToken {
		t.Fatal("Source served a token from memory instead of re-reading the file")
	}
	if err := store.Update(context.Background(), func(file *File) error {
		delete(file.Servers, f.srv.URL)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if tok, _ := src.Token(context.Background()); tok != "" {
		t.Fatal("Source still served a token after logout")
	}
}

func TestStatusAndLogoutNeverPrintTokens(t *testing.T) {
	f := newFakeOIDC(t)
	store := testStore(t)
	e := seed(t, store, f, time.Now().Add(30*time.Minute))
	var out, errOut bytes.Buffer
	c := &Commands{Stdout: &out, Stderr: &errOut, Store: store, Now: time.Now}

	if code := c.Status(context.Background(), nil); code != 0 {
		t.Fatalf("status exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"server:   " + f.srv.URL, "user:     admin@example.com", "expires:  "} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), e.IDToken) || strings.Contains(out.String(), e.RefreshToken) {
		t.Fatal("status printed a token")
	}
	statusOut := out.String()

	out.Reset()
	if code := c.Logout(context.Background(), []string{"--server", f.srv.URL}); code != 0 {
		t.Fatalf("logout exit %d", code)
	}
	file, _ := store.Load()
	if _, ok := file.Servers[f.srv.URL]; ok {
		t.Fatal("logout left the entry")
	}
	logoutOut := out.String()
	out.Reset()
	if code := c.Status(context.Background(), []string{"--server", f.srv.URL}); code != exitNotSigned {
		t.Fatalf("status after logout exit %d, want %d", code, exitNotSigned)
	}
	assertNoSecrets(t, f, statusOut, logoutOut, out.String(), errOut.String())
}

func TestLogoutAll(t *testing.T) {
	store := testStore(t)
	if err := store.Update(context.Background(), func(f *File) error {
		f.Servers["http://localhost:8080"] = &Entry{IDToken: "a"}
		f.Servers["https://topology.example"] = &Entry{IDToken: "b"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &Commands{Stdout: &out, Stderr: &out, Store: store}
	if code := c.Logout(context.Background(), []string{"--all"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	file, _ := store.Load()
	if len(file.Servers) != 0 {
		t.Fatalf("left %v", file.Servers)
	}
	if strings.Contains(out.String(), "a\n") || !strings.Contains(out.String(), "https://topology.example") {
		t.Fatalf("output %q", out.String())
	}
}

func TestClientForUsesTheSavedCA(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	store, err := DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(f *File) error {
		f.Servers["https://t.example"] = &Entry{CAFile: filepath.Join(t.TempDir(), "missing.pem")}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The saved CA path is used: it does not exist, so building the client fails on it.
	if _, _, err := ClientFor("https://t.example/", ""); err == nil || !strings.Contains(err.Error(), "--ca-file") {
		t.Fatalf("err = %v, want the saved CA to be read", err)
	}
	_, src, err := ClientFor("http://localhost:8080", "")
	if err != nil || src.Server != "http://localhost:8080" {
		t.Fatalf("ClientFor = %v, %v", src, err)
	}
}
