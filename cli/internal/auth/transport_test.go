package auth

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// T-15.6 — no token, code, verifier or refresh token over plain HTTP to a non-loopback host; no
// way to disable TLS verification.

func TestCheckSecretURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"http://localhost:8080":         true,
		"http://LOCALHOST:8080/dex":     true,
		"http://127.0.0.1:8080":         true,
		"http://127.10.0.3/token":       true,
		"http://[::1]:8080":             true,
		"https://topology.example.com":  true,
		"https://10.0.0.5/dex/token":    true,
		"http://topology.example.com":   false,
		"http://10.0.0.5:8080":          false,
		"http://192.168.1.10/dex/token": false,
		"http://localhost.example.com":  false,
		"http://127.0.0.1.nip.io:8080":  false,
		"ftp://localhost/":              false,
		"localhost:8080":                false,
	} {
		err := CheckSecretURL(raw)
		if (err == nil) != ok {
			t.Errorf("CheckSecretURL(%q) = %v, want allowed=%v", raw, err, ok)
		}
	}
	if err := CheckSecretURL("http://10.0.0.5"); !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("err = %v, want ErrInsecureTransport", err)
	}
}

func TestSignInRefusesPlainHTTPEndpoints(t *testing.T) {
	requireFreePorts(t)
	f := newFakeOIDC(t)
	f.tokenEndpoint = "http://192.0.2.10/dex/token" // TEST-NET-1: never routed
	opened := false
	_, err := signIn(t, f, testStore(t), SignInOptions{OpenBrowser: func(string) error { opened = true; return nil }})
	if !errors.Is(err, ErrInsecureTransport) || !strings.Contains(err.Error(), "token endpoint") {
		t.Fatalf("err = %v, want a transport refusal for the token endpoint", err)
	}
	if opened || len(f.authQueries) != 0 {
		t.Fatal("the sign-in started although its code would go over plain HTTP")
	}

	f.tokenEndpoint = ""
	_, err = signIn(t, f, testStore(t), SignInOptions{Issuer: "http://192.0.2.10/dex"})
	if !errors.Is(err, ErrInsecureTransport) || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("err = %v, want a transport refusal for the issuer", err)
	}
}

func TestSourceRefusesPlainHTTPServer(t *testing.T) {
	store := testStore(t)
	server := "http://192.0.2.10:8080"
	if err := store.Update(context.Background(), func(f *File) error {
		f.Servers[server] = &Entry{IDToken: "id-secret", Expiry: time.Now().Add(time.Hour)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tok, err := (&Source{Store: store, Server: server}).Token(context.Background())
	if tok != "" || !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("Token = %q, %v; want no token and a transport refusal", tok, err)
	}
	if strings.Contains(err.Error(), "id-secret") {
		t.Fatal("the error quotes the token")
	}
}

func TestRefreshRefusesPlainHTTPTokenEndpoint(t *testing.T) {
	f := newFakeOIDC(t)
	store := testStore(t)
	seed(t, store, f, time.Now().Add(-time.Minute))
	f.tokenEndpoint = "http://192.0.2.10/dex/token"
	_, err := (&Source{Store: store, Server: f.srv.URL}).Token(context.Background())
	if !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("err = %v, want a transport refusal", err)
	}
	if f.tokenRequests != 0 {
		t.Fatal("a token request was made")
	}
	file, _ := store.Load()
	if !file.Servers[f.srv.URL].SignedIn() {
		t.Fatal("a refusal to send cleared the stored sign-in")
	}
}

func TestRedirectToPlainHTTPIsNotFollowed(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 307 keeps the method and body: a token request would be replayed to the new host.
		http.Redirect(w, r, "http://192.0.2.10/token", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	client, err := NewHTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(redirector.URL, "application/x-www-form-urlencoded", strings.NewReader("refresh_token=x"))
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrInsecureTransport) {
		t.Fatalf("err = %v; want the redirect refused before it is followed", err)
	}
}

func TestCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()

	plain, err := NewHTTPClient("")
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := plain.Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a private certificate was accepted without --ca-file")
	}

	ca := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	withCA, err := NewHTTPClient(ca)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := withCA.Get(srv.URL)
	if err != nil {
		t.Fatalf("--ca-file not trusted: %v", err)
	}
	_ = resp.Body.Close()

	if err := os.WriteFile(ca, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHTTPClient(ca); err == nil {
		t.Fatal("a --ca-file without a certificate was accepted")
	}
}

func TestNoFlagDisablesTLSVerification(t *testing.T) {
	var buf bytes.Buffer
	c := &Commands{Stdout: &buf, Stderr: &buf, Store: testStore(t)}
	for name, run := range map[string]func([]string) int{
		"login":  func(a []string) int { return c.Login(context.Background(), a) },
		"logout": func(a []string) int { return c.Logout(context.Background(), a) },
		"status": func(a []string) int { return c.Status(context.Background(), a) },
	} {
		buf.Reset()
		if code := run([]string{"--insecure-skip-tls-verify"}); code != exitUsage {
			t.Errorf("%s accepted --insecure-skip-tls-verify (exit %d)", name, code)
		}
	}
	// The flag set is built inside Login; -h lists every flag it has.
	buf.Reset()
	_ = c.Login(context.Background(), []string{"-h"})
	for _, word := range []string{"insecure", "skip", "verify"} {
		if strings.Contains(strings.ToLower(buf.String()), word) {
			t.Fatalf("login has a flag mentioning %q:\n%s", word, buf.String())
		}
	}
}

// No source file in the module turns certificate verification off.
func TestNoTLSVerificationBypassInModule(t *testing.T) {
	forbidden := "Insecure" + "SkipVerify"
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(forbidden)) {
			t.Errorf("%s mentions %s", p, forbidden)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:8080":   "http://localhost:8080",
		"http://localhost:8080/":  "http://localhost:8080",
		" https://t.example/ui/ ": "https://t.example/ui",
	} {
		if got, err := NormalizeServer(in); err != nil || got != want {
			t.Errorf("NormalizeServer(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"localhost:8080", "", "ftp://x", "http://u:p@x", "http://x/?q=1"} {
		if _, err := NormalizeServer(bad); err == nil {
			t.Errorf("NormalizeServer(%q) accepted", bad)
		}
	}
}

// writeCA writes a valid PEM CA (an httptest server's certificate) to path.
func writeCA(t *testing.T, path string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	srv.Close()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// D-15.14: a relative --ca-file is saved as the absolute path login resolved, so a later command
// run from another directory cannot be made to trust a same-named file there.
func TestCAFileIsSavedAbsolute(t *testing.T) {
	requireFreePorts(t)
	f := newFakeOIDC(t)
	store := testStore(t)
	dir := t.TempDir()
	writeCA(t, filepath.Join(dir, "ca.pem"))
	t.Chdir(dir)
	if _, err := signIn(t, f, store, SignInOptions{CAFile: "ca.pem"}); err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	t.Chdir(t.TempDir())
	file, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	saved := file.Servers[f.srv.URL].CAFile
	if !filepath.IsAbs(saved) {
		t.Fatalf("saved CA path %q is relative", saved)
	}
	want, _ := os.Stat(filepath.Join(dir, "ca.pem"))
	if got, err := os.Stat(saved); err != nil || !os.SameFile(got, want) {
		t.Fatalf("saved CA path %q is not the file login was given (%v)", saved, err)
	}
}

// A relative path in the file (written by hand, or by an older build) is refused, never resolved
// against the current directory, by both the read path and the refresh.
func TestStoredRelativeCAFileIsRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeCA(t, filepath.Join(dir, "ca.pem"))
	t.Chdir(dir) // a file of that name exists here, and must still not be trusted
	store, err := DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	const server = "http://localhost:8080"
	if err := store.Update(context.Background(), func(f *File) error {
		f.Servers[server] = &Entry{Issuer: server + "/dex", ClientID: "topology-cli", CAFile: "ca.pem",
			IDToken: "id", Expiry: time.Now().Add(-time.Minute), RefreshToken: "r"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ClientFor(server, ""); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("ClientFor err = %v, want the relative CA refused", err)
	}
	src := &Source{Store: store, Server: server}
	if _, err := src.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("refresh err = %v, want the relative CA refused", err)
	}
}
