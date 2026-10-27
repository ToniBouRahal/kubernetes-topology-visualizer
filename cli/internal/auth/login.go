package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// CallbackPorts are the loopback ports registered as the public client's redirect URIs, tried in
// order (D-15.7). They are fixed so Dex can refuse every other redirect for the client and the
// Dex CSP can name the three origins.
var CallbackPorts = []int{38085, 38086, 38087}

const (
	// CallbackTimeout is how long login waits for the browser to come back (D-15.8 step 4).
	CallbackTimeout = 5 * time.Minute

	callbackPath    = "/callback"
	DefaultServer   = "http://localhost:8080"
	DefaultClientID = "topology-cli"
)

// SignInOptions configure one sign-in. Zero values take the D-15.8/D-15.9 defaults; Ports,
// Timeout and OpenBrowser exist so tests can drive the flow.
type SignInOptions struct {
	Server   string
	Issuer   string // default: probed, <Server>/keycloak/realms/topology then <Server>/dex (ADR-017 D-17.8)
	ClientID string // default topology-cli
	Scopes   []string
	CAFile   string

	Ports       []int
	Timeout     time.Duration
	OpenBrowser func(url string) error
	// Prompt receives the authorisation URL, for when no browser can be opened. It never
	// receives a token, code or verifier.
	Prompt io.Writer
}

// SignInResult describes a completed login. NoAuth means the server answered without a token
// and nothing was stored (D-15.13).
type SignInResult struct {
	Server string
	Email  string
	Expiry time.Time
	NoAuth bool
}

// SignIn runs D-15.8: probe for auth-off, discover, listen on a loopback port, send the browser
// to the provider with a PKCE S256 challenge, take one callback whose state matches, redeem the
// code with the verifier, and store the tokens.
func SignIn(ctx context.Context, store *Store, o SignInOptions) (*SignInResult, error) {
	server, err := NormalizeServer(o.Server)
	if err != nil {
		return nil, err
	}
	issuer := strings.TrimRight(o.Issuer, "/")
	clientID := o.ClientID
	if clientID == "" {
		clientID = DefaultClientID
	}
	ports := o.Ports
	if len(ports) == 0 {
		ports = CallbackPorts
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = CallbackTimeout
	}
	prompt := o.Prompt
	if prompt == nil {
		prompt = io.Discard
	}

	// Saved absolute: later commands, refreshes and the MCP server run from other directories.
	caFile, err := absCAFile(o.CAFile)
	if err != nil {
		return nil, err
	}
	client, err := NewHTTPClient(caFile)
	if err != nil {
		return nil, err
	}
	required, err := requiresSignIn(ctx, client, server)
	if err != nil {
		return nil, err
	}
	if !required {
		return &SignInResult{Server: server, NoAuth: true}, nil
	}
	// Every later request carries the ID token to the server.
	if err := CheckSecretURL(server); err != nil {
		return nil, fmt.Errorf("server: %w", err)
	}
	var disc *discovery
	if issuer == "" {
		issuer, disc, err = probeIssuer(ctx, client, server)
	} else {
		disc, err = discover(ctx, client, issuer)
	}
	if err != nil {
		return nil, err
	}
	scopes := o.Scopes
	if len(scopes) == 0 {
		scopes = defaultScopes(disc)
	}

	ln, err := listenLoopback(ports)
	if err != nil {
		return nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	conf := &oauth2.Config{
		ClientID: clientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:   disc.AuthorizationEndpoint,
			TokenURL:  disc.TokenEndpoint,
			AuthStyle: oauth2.AuthStyleInParams, // public client: client_id in the body, no secret
		},
		RedirectURL: "http://127.0.0.1:" + strconv.Itoa(port) + callbackPath,
		Scopes:      scopes,
	}
	verifier := oauth2.GenerateVerifier()
	state, nonce := randomString(), randomString()
	authURL := conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce))

	code, err := awaitCallback(ctx, ln, state, timeout, func() {
		fmt.Fprintf(prompt, "Opening the browser to sign in to %s. If it does not open, visit:\n\n  %s\n\n", server, authURL)
		if o.OpenBrowser != nil {
			if err := o.OpenBrowser(authURL); err != nil {
				fmt.Fprintln(prompt, "Could not open a browser; open the URL above by hand.")
			}
		}
	})
	if err != nil {
		return nil, err
	}

	tok, err := conf.Exchange(context.WithValue(ctx, oauth2.HTTPClient, client), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, describeTokenError("redeeming the sign-in code", err)
	}
	idToken, _ := tok.Extra("id_token").(string)
	if idToken == "" {
		return nil, errors.New("the provider returned no ID token; is the openid scope granted?")
	}
	c, err := checkIDToken(idToken, issuer, clientID, nonce)
	if err != nil {
		return nil, err
	}

	err = store.Update(ctx, func(f *File) error {
		f.Servers[server] = &Entry{
			Issuer:       issuer,
			ClientID:     clientID,
			Scopes:       scopes,
			CAFile:       caFile,
			IDToken:      idToken,
			Expiry:       c.expiry(),
			RefreshToken: tok.RefreshToken,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &SignInResult{Server: server, Email: c.Email, Expiry: c.expiry()}, nil
}

// absCAFile resolves a --ca-file path against the working directory login runs in.
func absCAFile(caFile string) (string, error) {
	if caFile == "" {
		return "", nil
	}
	abs, err := filepath.Abs(caFile)
	if err != nil {
		return "", fmt.Errorf("resolving --ca-file: %w", err)
	}
	return abs, nil
}

// requiresSignIn asks GET /api/v1/namespaces without a token (D-15.13). Redirects are not
// followed: a redirect to a sign-in page must not read as a 200.
func requiresSignIn(ctx context.Context, client *http.Client, server string) (bool, error) {
	probe := *client
	probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/api/v1/namespaces", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := probe.Do(req)
	if err != nil {
		return false, &ConnectionError{Target: server, Err: err}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return false, nil
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden,
		resp.StatusCode >= 300 && resp.StatusCode < 400:
		return true, nil
	default:
		return false, fmt.Errorf("%s answered %s to GET /api/v1/namespaces; is --server the topology UI URL?",
			server, resp.Status)
	}
}

// defaultIssuerPaths are the bundled providers' issuer paths under the server, probed in order when
// login has no --issuer (ADR-017 D-17.8): Keycloak first, then Dex.
var defaultIssuerPaths = []string{"/keycloak/realms/topology", "/dex"}

// probeIssuer returns the first default issuer whose discovery document is valid and names that
// issuer (ADR-017 D-17.8). Redirects are not followed: in Dex mode the Keycloak path answers with
// oauth2-proxy's sign-in redirect, which must read as "not here" rather than be chased to a
// sign-in page or a host the CLI cannot reach. A connection failure stops the probe at once, as
// does a document that would send a secret over plain HTTP.
func probeIssuer(ctx context.Context, client *http.Client, server string) (string, *discovery, error) {
	probe := *client
	probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	candidates := make([]string, len(defaultIssuerPaths))
	failures := make([]string, len(defaultIssuerPaths))
	for i, path := range defaultIssuerPaths {
		candidates[i] = server + path
		disc, err := discover(ctx, &probe, candidates[i])
		if err == nil {
			return candidates[i], disc, nil
		}
		var ce *ConnectionError
		if errors.As(err, &ce) || errors.Is(err, ErrInsecureTransport) {
			return "", nil, err
		}
		failures[i] = err.Error()
	}
	return "", nil, fmt.Errorf("found no OIDC issuer at %s (%s); pass --issuer",
		strings.Join(candidates, " or "), strings.Join(failures, "; "))
}

// listenLoopback binds the first free port on 127.0.0.1 — never a wildcard address, so nothing
// off the machine can deliver a callback.
func listenLoopback(ports []int) (net.Listener, error) {
	var lastErr error
	for _, p := range ports {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err == nil {
			return ln, nil
		}
		lastErr = err
	}
	names := make([]string, len(ports))
	for i, p := range ports {
		names[i] = "127.0.0.1:" + strconv.Itoa(p)
	}
	return nil, fmt.Errorf("cannot listen for the sign-in callback: %s are all in use (%v); stop whatever holds one and retry",
		strings.Join(names, ", "), lastErr)
}

// awaitCallback serves ln until one request on /callback carries the expected state, then shuts
// the listener down. Requests with another state are refused and waiting continues, so a stray
// or forged request cannot end the sign-in. ready runs once the listener is serving.
func awaitCallback(ctx context.Context, ln net.Listener, state string, timeout time.Duration, ready func()) (string, error) {
	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	var (
		mu   sync.Mutex
		done bool
	)
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		q := r.URL.Query()
		mu.Lock()
		defer mu.Unlock()
		if done {
			http.Error(w, "This sign-in has already completed.", http.StatusGone)
			return
		}
		if q.Get("state") != state {
			http.Error(w, "State mismatch: this callback does not belong to the running sign-in.", http.StatusBadRequest)
			return
		}
		done = true
		if e := q.Get("error"); e != "" {
			msg := "the provider refused the sign-in: " + e
			if d := q.Get("error_description"); d != "" {
				msg += " (" + d + ")"
			}
			writePage(w, http.StatusBadRequest, "Sign-in failed", msg)
			results <- result{err: errors.New(msg)}
			return
		}
		code := q.Get("code")
		if code == "" {
			writePage(w, http.StatusBadRequest, "Sign-in failed", "The provider sent no code.")
			results <- result{err: errors.New("the provider's callback carried no code")}
			return
		}
		writePage(w, http.StatusOK, "Signed in", "You can close this tab and return to the terminal.")
		results <- result{code: code}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if srv.Shutdown(shutdownCtx) != nil {
			_ = srv.Close()
		}
	}()

	ready()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-results:
		return r.code, r.err
	case <-timer.C:
		return "", fmt.Errorf("gave up waiting for the browser after %s; run login again", timeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func writePage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1><p>%s</p>\n",
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(body))
}

func randomString() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error on supported platforms
	return base64.RawURLEncoding.EncodeToString(b)
}

// describeTokenError reports a token-endpoint failure by its OAuth error code and description
// only. oauth2.RetrieveError's own message includes the raw response body, which this package
// does not echo.
func describeTokenError(what string, err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		msg := what + ": the provider answered"
		if re.Response != nil {
			msg += " " + re.Response.Status
		}
		if re.ErrorCode != "" {
			msg += " " + re.ErrorCode
		}
		if re.ErrorDescription != "" {
			msg += " (" + re.ErrorDescription + ")"
		}
		return &refusedError{msg: msg}
	}
	var ce *ConnectionError
	if errors.As(err, &ce) || errors.Is(err, ErrInsecureTransport) {
		return err
	}
	return &ConnectionError{Target: "the token endpoint", Err: err}
}

// refusedError is a definitive "no" from the token endpoint, as opposed to not reaching it.
type refusedError struct{ msg string }

func (e *refusedError) Error() string { return e.msg }
