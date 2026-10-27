// Package api is the CLI's hand-written client for the backend's five read operations
// (ADR-015 D-15.3). There is deliberately no ingest method: the CLI cannot call what it does not
// have. Every method returns the decoded struct and the response body exactly as received, so
// "-o json" can print the payload unchanged.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxBody bounds what the client will read from one response. The backend caps its own graph
// responses well below this; the bound only stops a misbehaving server from exhausting memory.
const maxBody = 64 << 20

// Presets are the API's window presets. Anything else is sent as from/to.
var Presets = []string{"1m", "5m", "15m", "1h", "6h", "24h"}

// TokenSource supplies the bearer credential for each request. An empty token with a nil error
// means none is stored for this server: the request goes out without one, which an auth-off
// install answers (D-15.13). An error means a credential exists but cannot be used now (expired
// and not refreshable, unreadable file, issuer unreachable); the caller maps it by its cause.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Refresher is a TokenSource that can replace a token the server refused. Refresh returns the
// token to retry with once; rejected is the token the refused request carried, so a source
// shared between processes refreshes only if nobody has replaced it yet.
type Refresher interface {
	Refresh(ctx context.Context, rejected string) (string, error)
}

// NoToken is the TokenSource for a server with no stored credential.
type NoToken struct{}

func (NoToken) Token(context.Context) (string, error) { return "", nil }

// Window selects the period of a graph, namespaces, node or edge read. The zero Window sends
// nothing and lets the backend apply its default; the response's window says what was used.
type Window struct {
	Preset   string // one of Presets; exclusive with From/To
	From, To time.Time
}

// Period is an explicit half-open period [From, To).
type Period struct {
	From, To time.Time
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (w Window) encode(v url.Values) {
	switch {
	case w.Preset != "":
		v.Set("window", w.Preset)
	case !w.From.IsZero() || !w.To.IsZero():
		v.Set("from", formatTime(w.From))
		v.Set("to", formatTime(w.To))
	}
}

// GraphQuery holds the graph endpoint's filters.
type GraphQuery struct {
	Window            Window
	Namespaces        []string
	Kind              string
	Query             string
	ExcludeExternal   bool // the API's include_external defaults to true
	IncludeUnresolved bool
}

// DiffQuery holds the diff endpoint's two periods and filters.
type DiffQuery struct {
	Baseline, Current Period
	Namespaces        []string
}

// StatusError is a non-2xx answer. Code and Detail come from the ErrorResponse body when the
// server sent one.
type StatusError struct {
	Status int
	Code   string
	Detail string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("server answered %d %s", e.Status, http.StatusText(e.Status))
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unauthorized reports whether the server refused the request for want of a valid credential.
func (e *StatusError) Unauthorized() bool { return e.Status == http.StatusUnauthorized }

// ConnectionError is a failure to reach the server or read its answer at all.
type ConnectionError struct{ Err error }

func (e *ConnectionError) Error() string { return "cannot reach the server: " + e.Err.Error() }
func (e *ConnectionError) Unwrap() error { return e.Err }

// TokenError wraps a TokenSource failure. Its message is the source's, which must never contain
// the token itself (D-15.11).
type TokenError struct{ Err error }

func (e *TokenError) Error() string { return e.Err.Error() }
func (e *TokenError) Unwrap() error { return e.Err }

// Client reads one server. Server is the frontend URL a browser would use.
type Client struct {
	server *url.URL
	http   *http.Client
	tokens TokenSource
}

// New returns a client for server. httpClient carries the transport rules (CA, redirects); tokens
// may be nil, meaning NoToken.
func New(server string, httpClient *http.Client, tokens TokenSource) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(server, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid server URL %q: want http(s)://host[:port]", server)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if tokens == nil {
		tokens = NoToken{}
	}
	return &Client{server: u, http: httpClient, tokens: tokens}, nil
}

// Server is the URL this client reads, as given to New without a trailing slash.
func (c *Client) Server() string { return c.server.String() }

func (c *Client) Namespaces(ctx context.Context, w Window) (*NamespaceList, []byte, error) {
	v := url.Values{}
	w.encode(v)
	return get[NamespaceList](ctx, c, "/api/v1/namespaces", v)
}

func (c *Client) Graph(ctx context.Context, q GraphQuery) (*GraphResponse, []byte, error) {
	return get[GraphResponse](ctx, c, "/api/v1/graph", q.values())
}

// Anomalies reads GET /api/v1/anomalies, which takes the graph's window and filters (ADR-016
// D-16.3).
func (c *Client) Anomalies(ctx context.Context, q GraphQuery) (*AnomalyResponse, []byte, error) {
	return get[AnomalyResponse](ctx, c, "/api/v1/anomalies", q.values())
}

func (q GraphQuery) values() url.Values {
	v := url.Values{}
	q.Window.encode(v)
	for _, ns := range q.Namespaces {
		v.Add("namespace", ns)
	}
	if q.Kind != "" {
		v.Set("kind", q.Kind)
	}
	if q.Query != "" {
		v.Set("query", q.Query)
	}
	if q.ExcludeExternal {
		v.Set("include_external", "false")
	}
	if q.IncludeUnresolved {
		v.Set("include_unresolved", "true")
	}
	return v
}

// Node reads one node's dependencies. id is passed through verbatim, path-escaped only.
func (c *Client) Node(ctx context.Context, id string, w Window) (*NodeDetail, []byte, error) {
	v := url.Values{}
	w.encode(v)
	return get[NodeDetail](ctx, c, "/api/v1/nodes/"+url.PathEscape(id), v)
}

// Edge reads one edge. id is passed through verbatim, path-escaped only.
func (c *Client) Edge(ctx context.Context, id string, w Window) (*EdgeDetail, []byte, error) {
	v := url.Values{}
	w.encode(v)
	return get[EdgeDetail](ctx, c, "/api/v1/edges/"+url.PathEscape(id), v)
}

func (c *Client) Diff(ctx context.Context, q DiffQuery) (*DiffResponse, []byte, error) {
	v := url.Values{}
	v.Set("baseline_from", formatTime(q.Baseline.From))
	v.Set("baseline_to", formatTime(q.Baseline.To))
	v.Set("current_from", formatTime(q.Current.From))
	v.Set("current_to", formatTime(q.Current.To))
	for _, ns := range q.Namespaces {
		v.Add("namespace", ns)
	}
	return get[DiffResponse](ctx, c, "/api/v1/diff", v)
}

func get[T any](ctx context.Context, c *Client, path string, query url.Values) (*T, []byte, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, nil, &TokenError{Err: err}
	}
	// path is already escaped (an id segment went through PathEscape); RawPath keeps that form.
	u := *c.server
	u.RawPath = c.server.EscapedPath() + path
	if u.Path, err = url.PathUnescape(u.RawPath); err != nil {
		return nil, nil, err
	}
	u.RawQuery = query.Encode()

	status, body, err := c.do(ctx, u.String(), token)
	if status == http.StatusUnauthorized && token != "" {
		// The token was refused although it had not expired by the clock: Dex restarted, say.
		// Refresh once — unless another process already replaced that token — and retry (D-15.6).
		if r, ok := c.tokens.(Refresher); ok {
			fresh, rerr := r.Refresh(ctx, token)
			if rerr != nil {
				return nil, nil, &TokenError{Err: rerr}
			}
			status, body, err = c.do(ctx, u.String(), fresh)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	if status < 200 || status > 299 {
		return nil, body, statusError(status, body)
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, body, fmt.Errorf("decoding the %s response: %w", u.Path, err)
	}
	return &out, body, nil
}

// do sends one GET, with the token when there is one, and returns the status and body.
func (c *Client) do(ctx context.Context, target, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, &ConnectionError{Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, &ConnectionError{Err: err}
	}
	return resp.StatusCode, body, nil
}

// statusError reads an ErrorResponse when the body is one. FastAPI's own 422 has a list as
// "detail", and nginx's 401 may have any shape; those keep only the status.
func statusError(status int, body []byte) error {
	e := &StatusError{Status: status}
	var er ErrorResponse
	if json.Unmarshal(body, &er) == nil {
		e.Code, e.Detail = er.Error, er.Detail
	}
	return e
}

// IsUnauthorized reports whether err is a 401 from the server.
func IsUnauthorized(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Unauthorized()
}
