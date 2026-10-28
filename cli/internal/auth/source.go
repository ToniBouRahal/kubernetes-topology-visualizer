package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

const (
	// RefreshWindow: an ID token expiring within this is refreshed before use (D-15.10).
	RefreshWindow = 60 * time.Second

	// refreshTimeout bounds the discovery and token requests made while the lock is held, so a
	// waiting process's DefaultLockTimeout is not spent on one slow provider.
	refreshTimeout = 20 * time.Second
)

// NotSignedInError means there is no usable token for Server and only an interactive login can
// make one. Its message is the one D-15.5 has the MCP server return to the agent.
type NotSignedInError struct {
	Server string
	// Reason says why, for a caller that wants to show more (e.g. the refresh was refused). It
	// never holds a token.
	Reason string
}

func (e *NotSignedInError) Error() string {
	return fmt.Sprintf("not signed in to %s: run \"kubectl topology login --server %s\"", e.Server, e.Server)
}

// Source is the token source the API client consumes, one per server. It holds no token in
// memory: every call reads the credentials file, so a long-running MCP process sees a login,
// refresh or logout done by any other process (D-15.5).
type Source struct {
	Store  *Store
	Server string // as returned by NormalizeServer
	Now    func() time.Time
}

// NewSource returns a Source for server over the default credentials store.
func NewSource(server string) (*Source, error) {
	norm, err := NormalizeServer(server)
	if err != nil {
		return nil, err
	}
	store, err := DefaultStore()
	if err != nil {
		return nil, err
	}
	return &Source{Store: store, Server: norm}, nil
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Token returns the ID token to send as "Authorization: Bearer", refreshing it first if it
// expires within RefreshWindow. It returns "" and no error when nothing was ever stored for the
// server: the request then goes without a token, which an auth-off install answers (D-15.13).
func (s *Source) Token(ctx context.Context) (string, error) {
	return s.get(ctx, "")
}

// Refresh is for after the server answered 401 to rejected (the token a request carried): it
// refreshes unless another process has already replaced that token, and returns the token to
// retry with once. A NotSignedInError means the user must run login.
func (s *Source) Refresh(ctx context.Context, rejected string) (string, error) {
	tok, err := s.get(ctx, rejected)
	if err == nil && tok == "" {
		// Nothing stored, yet the server wants a token.
		return "", &NotSignedInError{Server: s.Server, Reason: "no credentials are stored for this server"}
	}
	return tok, err
}

// get implements both calls; rejected != "" forces a refresh of that exact token.
func (s *Source) get(ctx context.Context, rejected string) (string, error) {
	// Lock-free fast path: the file is replaced by rename, so this read is never torn. Only a
	// token that needs refreshing takes the lock (and is then re-read under it).
	f, err := s.Store.Load()
	if err != nil {
		return "", err
	}
	e := f.Servers[s.Server]
	if e == nil {
		return "", nil
	}
	if !e.SignedIn() {
		return "", &NotSignedInError{Server: s.Server, Reason: "the stored sign-in was cleared"}
	}
	if err := CheckSecretURL(s.Server); err != nil {
		return "", fmt.Errorf("server: %w", err)
	}
	if !s.needsRefresh(e, rejected) {
		return e.IDToken, nil
	}

	var token string
	var outcome error
	err = s.Store.Update(ctx, func(f *File) error {
		e := f.Servers[s.Server]
		switch {
		case e == nil:
			// Logged out meanwhile.
			outcome = &NotSignedInError{Server: s.Server, Reason: "signed out"}
			return errUnchanged
		case !e.SignedIn():
			outcome = &NotSignedInError{Server: s.Server, Reason: "the stored sign-in was cleared"}
			return errUnchanged
		case !s.needsRefresh(e, rejected):
			// Another process refreshed while this one waited for the lock.
			token = e.IDToken
			return errUnchanged
		case e.RefreshToken == "":
			e.clearTokens()
			outcome = &NotSignedInError{Server: s.Server, Reason: "the token expired and there is no refresh token"}
			return nil
		}
		failed := e.RefreshToken
		next, err := s.refresh(ctx, e)
		if err != nil {
			var refused *refusedError
			if !errors.As(err, &refused) {
				// Could not ask: keep the tokens, a later call may get through.
				return err
			}
			// Compare-before-clear: re-read the file and clear only if it still holds the
			// refresh token that was just refused, so a rotation written by anyone else since
			// is not thrown away (D-15.10).
			fresh, rerr := s.Store.read()
			if rerr != nil {
				return rerr
			}
			*f = *fresh
			if clearIfRefreshTokenIs(f, s.Server, failed) {
				outcome = &NotSignedInError{Server: s.Server, Reason: err.Error()}
				return nil
			}
			if e := f.Servers[s.Server]; e.SignedIn() {
				token = e.IDToken
				return errUnchanged
			}
			outcome = &NotSignedInError{Server: s.Server, Reason: "signed out"}
			return errUnchanged
		}
		*e = *next
		token = e.IDToken
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, outcome
}

func (s *Source) needsRefresh(e *Entry, rejected string) bool {
	if rejected != "" {
		return e.IDToken == rejected
	}
	return !s.now().Add(RefreshWindow).Before(e.Expiry)
}

// refresh redeems e's refresh token and returns the updated entry. Dex rotates refresh tokens by
// default, so the new one replaces the old; a provider that returns none keeps the old one.
func (s *Source) refresh(ctx context.Context, e *Entry) (*Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()
	caFile, err := e.caFile()
	if err != nil {
		return nil, err
	}
	client, err := NewHTTPClient(caFile)
	if err != nil {
		return nil, err
	}
	disc, err := discover(ctx, client, e.Issuer)
	if err != nil {
		return nil, err
	}
	conf := &oauth2.Config{
		ClientID: e.ClientID,
		Endpoint: oauth2.Endpoint{TokenURL: disc.TokenEndpoint, AuthStyle: oauth2.AuthStyleInParams},
		Scopes:   e.Scopes,
	}
	// An empty access token makes the oauth2 source refresh immediately.
	tok, err := conf.TokenSource(context.WithValue(ctx, oauth2.HTTPClient, client),
		&oauth2.Token{RefreshToken: e.RefreshToken}).Token()
	if err != nil {
		return nil, describeTokenError("refreshing the sign-in", err)
	}
	idToken, _ := tok.Extra("id_token").(string)
	if idToken == "" {
		return nil, &refusedError{msg: "refreshing the sign-in: the provider returned no ID token"}
	}
	c, err := checkIDToken(idToken, e.Issuer, e.ClientID, "")
	if err != nil {
		return nil, &refusedError{msg: "refreshing the sign-in: " + err.Error()}
	}
	next := *e
	next.IDToken = idToken
	next.Expiry = c.expiry()
	if tok.RefreshToken != "" {
		next.RefreshToken = tok.RefreshToken
	}
	return &next, nil
}

// clearIfRefreshTokenIs drops server's tokens only if its stored refresh token is still failed.
// It reports whether it cleared.
func clearIfRefreshTokenIs(f *File, server, failed string) bool {
	e := f.Servers[server]
	if e == nil || e.RefreshToken != failed {
		return false
	}
	e.clearTokens()
	return true
}

// ClientFor returns the HTTP client and token source for one server, for the read commands and
// the MCP server to hand the API client. caFile, when empty, falls back to the CA saved with the
// server's sign-in, so later commands need at most --server (D-15.9).
func ClientFor(server, caFile string) (*http.Client, *Source, error) {
	src, err := NewSource(server)
	if err != nil {
		return nil, nil, err
	}
	if caFile == "" {
		if f, err := src.Store.Load(); err == nil {
			if e := f.Servers[src.Server]; e != nil {
				if caFile, err = e.caFile(); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	client, err := NewHTTPClient(caFile)
	if err != nil {
		return nil, nil, err
	}
	return client, src, nil
}
