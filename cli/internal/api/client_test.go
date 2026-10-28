package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

// fakeSource hands out tok and, after a 401, replaces it once with fresh (or fails with err).
type fakeSource struct {
	mu        sync.Mutex
	tok       string
	fresh     string
	err       error
	refreshes []string
}

func (f *fakeSource) Token(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tok, nil
}

func (f *fakeSource) Refresh(_ context.Context, rejected string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes = append(f.refreshes, rejected)
	if f.err != nil {
		return "", f.err
	}
	f.tok = f.fresh
	return f.fresh, nil
}

// accepting answers 200 with the namespaces fixture to the token good, 401 to anything else,
// and records each request's Authorization header.
func accepting(t *testing.T, good string) (*httptest.Server, *[]string) {
	body, err := os.ReadFile("../../testdata/namespaces.json")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+good {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized","detail":"sign in"}`))
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// D-15.6: a token refused before its expiry (a Dex restart) is refreshed once and the request
// retried; only if that fails does the caller see "not signed in".
func TestRefusedTokenIsRefreshedAndRetriedOnce(t *testing.T) {
	srv, seen := accepting(t, "new")
	src := &fakeSource{tok: "old", fresh: "new"}
	c, err := New(srv.URL, nil, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Namespaces(context.Background(), Window{}); err != nil {
		t.Fatal(err)
	}
	if len(src.refreshes) != 1 || src.refreshes[0] != "old" || len(*seen) != 2 || (*seen)[1] != "Bearer new" {
		t.Errorf("refreshes %v, requests %v", src.refreshes, *seen)
	}

	// The refreshed token is refused too: a 401, not a loop.
	srv2, seen2 := accepting(t, "never")
	src2 := &fakeSource{tok: "old", fresh: "new"}
	c2, _ := New(srv2.URL, nil, src2)
	if _, _, err := c2.Namespaces(context.Background(), Window{}); !IsUnauthorized(err) || len(*seen2) != 2 {
		t.Errorf("err %v after %d requests, want a 401 after 2", err, len(*seen2))
	}

	// The refresh fails: a TokenError, so the command says "run login".
	src3 := &fakeSource{tok: "old", err: errors.New("refresh refused")}
	c3, _ := New(srv2.URL, nil, src3)
	var te *TokenError
	if _, _, err := c3.Namespaces(context.Background(), Window{}); !errors.As(err, &te) {
		t.Errorf("err %v, want a TokenError", err)
	}
}

// D-15.13: with nothing stored no token is sent, and a 401 is not "refreshed".
func TestNoTokenNoRefresh(t *testing.T) {
	srv, seen := accepting(t, "x")
	src := &fakeSource{}
	c, _ := New(srv.URL, nil, src)
	if _, _, err := c.Namespaces(context.Background(), Window{}); !IsUnauthorized(err) {
		t.Errorf("err %v, want 401", err)
	}
	if len(src.refreshes) != 0 || len(*seen) != 1 || (*seen)[0] != "" {
		t.Errorf("refreshes %v, requests %v", src.refreshes, *seen)
	}
}
