package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOIDC is the topology server and its bundled Dex in one httptest server: /api/v1/namespaces
// answers 401 (or 200 with authOff), /dex/* is a minimal provider that enforces PKCE S256 the
// way the CLI relies on and rotates refresh tokens like Dex.
type fakeOIDC struct {
	t   *testing.T
	srv *httptest.Server

	mu              sync.Mutex
	authOff         bool
	scopesSupported []string
	tokenEndpoint   string // overrides the advertised token endpoint when set
	email           string
	lifetime        time.Duration
	refreshDelay    time.Duration
	refuseRefresh   bool
	onRefresh       func() // runs inside the refresh handler, before it answers

	authQueries   []url.Values
	pending       map[string]authRequest // code -> request
	issued        []string               // every code, verifier, ID and refresh token handed out
	refreshToken  string                 // the one currently valid
	refreshes     int
	tokenRequests int
	serial        int
}

type authRequest struct {
	challenge, clientID, redirectURI, nonce string
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	f := &fakeOIDC{t: t, email: "admin@example.com", lifetime: time.Hour, pending: map[string]authRequest{},
		scopesSupported: []string{"openid", "email", "profile", "offline_access"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces", f.namespaces)
	mux.HandleFunc("/dex/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/dex/auth", f.authorize)
	mux.HandleFunc("/dex/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOIDC) issuer() string { return f.srv.URL + "/dex" }

func (f *fakeOIDC) namespaces(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	off := f.authOff
	f.mu.Unlock()
	if off || r.Header.Get("Authorization") != "" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"namespaces":[]}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"detail":"sign-in required"}`))
}

func (f *fakeOIDC) discovery(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tokenURL := f.issuer() + "/token"
	if f.tokenEndpoint != "" {
		tokenURL = f.tokenEndpoint
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                 f.issuer(),
		"authorization_endpoint": f.issuer() + "/auth",
		"token_endpoint":         tokenURL,
		"scopes_supported":       f.scopesSupported,
	})
}

// authorize stands in for the Dex login page: it records the request and redirects straight to
// the callback, as Dex's 303 after the password POST does.
func (f *fakeOIDC) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.authQueries = append(f.authQueries, q)
	f.serial++
	code := fmt.Sprintf("code-%d-secret", f.serial)
	f.pending[code] = authRequest{challenge: q.Get("code_challenge"), clientID: q.Get("client_id"),
		redirectURI: q.Get("redirect_uri"), nonce: q.Get("nonce")}
	f.issued = append(f.issued, code)
	f.mu.Unlock()
	if q.Get("code_challenge_method") != "S256" {
		http.Error(w, "S256 required", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, q.Get("redirect_uri")+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state")),
		http.StatusSeeOther)
}

func (f *fakeOIDC) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.tokenRequests++
	f.mu.Unlock()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		f.mu.Lock()
		req, ok := f.pending[r.PostForm.Get("code")]
		delete(f.pending, r.PostForm.Get("code"))
		verifier := r.PostForm.Get("code_verifier")
		f.issued = append(f.issued, verifier)
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(verifier))
		switch {
		case !ok:
			oauthError(w, "invalid_grant", "unknown code")
		case base64.RawURLEncoding.EncodeToString(sum[:]) != req.challenge:
			oauthError(w, "invalid_grant", "PKCE verifier does not match the challenge")
		case r.PostForm.Get("client_id") != req.clientID || r.PostForm.Get("redirect_uri") != req.redirectURI:
			oauthError(w, "invalid_grant", "client or redirect mismatch")
		case r.PostForm.Get("client_secret") != "":
			oauthError(w, "invalid_client", "public client sent a secret")
		default:
			f.answer(w, req.clientID, req.nonce)
		}
	case "refresh_token":
		f.mu.Lock()
		delay, refuse, hook := f.refreshDelay, f.refuseRefresh, f.onRefresh
		f.mu.Unlock()
		time.Sleep(delay)
		if hook != nil {
			hook()
		}
		f.mu.Lock()
		valid := !refuse && r.PostForm.Get("refresh_token") == f.refreshToken
		if valid {
			f.refreshes++
		}
		f.mu.Unlock()
		if !valid {
			oauthError(w, "invalid_grant", "refresh token is invalid or has already been claimed")
			return
		}
		f.answer(w, r.PostForm.Get("client_id"), "")
	default:
		oauthError(w, "unsupported_grant_type", "")
	}
}

func (f *fakeOIDC) answer(w http.ResponseWriter, clientID, nonce string) {
	f.mu.Lock()
	f.serial++
	id := makeJWT(map[string]any{"iss": f.issuer(), "aud": clientID, "exp": time.Now().Add(f.lifetime).Unix(),
		"email": f.email, "nonce": nonce, "jti": f.serial})
	f.refreshToken = fmt.Sprintf("refresh-%d-secret", f.serial)
	f.issued = append(f.issued, id, f.refreshToken)
	rt := f.refreshToken
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-secret", "token_type": "bearer",
		"expires_in": 3600, "id_token": id, "refresh_token": rt})
}

func oauthError(w http.ResponseWriter, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

func (f *fakeOIDC) secrets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{"access-secret"}, f.issued...)
}

// browser follows the authorisation URL, and so the redirect to the loopback callback, as the
// user's browser would.
func browser(t *testing.T) func(string) error {
	return func(u string) error {
		go func() {
			resp, err := http.Get(u)
			if err != nil {
				t.Errorf("browser: %v", err)
				return
			}
			_ = resp.Body.Close()
		}()
		return nil
	}
}

func makeJWT(claims map[string]any) string {
	enc := base64.RawURLEncoding
	payload, _ := json.Marshal(claims)
	return enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(payload) + "." +
		enc.EncodeToString([]byte("not-a-signature"))
}

func assertNoSecrets(t *testing.T, f *fakeOIDC, outputs ...string) {
	t.Helper()
	all := strings.Join(outputs, "\n")
	for _, s := range f.secrets() {
		if s != "" && strings.Contains(all, s) {
			t.Fatalf("output contains a credential (%.12s…):\n%s", s, all)
		}
	}
}
