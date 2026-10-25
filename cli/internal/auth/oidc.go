package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// discovery is the part of an OpenID Provider's configuration document the CLI uses.
type discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

// discover fetches <issuer>/.well-known/openid-configuration and applies the transport rule to the
// endpoints it advertises (D-15.8 step 1, D-15.14): the authorisation endpoint receives the
// challenge and the token endpoint the code, verifier and refresh token.
func discover(ctx context.Context, client *http.Client, issuer string) (*discovery, error) {
	issuer = strings.TrimRight(issuer, "/")
	// Discovery carries no credential, but a document fetched over plain HTTP from a non-loopback
	// host could be rewritten in transit to point the token endpoint anywhere.
	if err := CheckSecretURL(issuer); err != nil {
		return nil, fmt.Errorf("issuer: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &ConnectionError{Target: "issuer " + issuer, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("issuer %s: discovery answered %s; check --issuer", issuer, resp.Status)
	}
	var d discovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, fmt.Errorf("issuer %s: discovery document is not valid JSON", issuer)
	}
	if strings.TrimRight(d.Issuer, "/") != issuer {
		return nil, fmt.Errorf("issuer %s: discovery names a different issuer %q; check --issuer", issuer, d.Issuer)
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" {
		return nil, fmt.Errorf("issuer %s: discovery lacks an authorization or token endpoint", issuer)
	}
	if err := CheckSecretURL(d.AuthorizationEndpoint); err != nil {
		return nil, fmt.Errorf("authorization endpoint: %w", err)
	}
	if err := CheckSecretURL(d.TokenEndpoint); err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	return &d, nil
}

// defaultScopes is D-15.8's scope set: groups only when the provider says it knows the scope, so
// an allowedGroups install sees a groups claim and a provider without one is not sent an unknown
// scope.
func defaultScopes(d *discovery) []string {
	scopes := []string{"openid", "email", "profile", "offline_access"}
	if slices.Contains(d.ScopesSupported, "groups") {
		scopes = append(scopes, "groups")
	}
	return scopes
}

// claims are the ID-token fields the CLI reads. The signature is not checked here: the CLI is
// not the relying party — oauth2-proxy verifies every request's token — and these claims only
// drive the expiry clock, `status`, and sanity checks against a mixed-up provider.
type claims struct {
	Issuer   string          `json:"iss"`
	Audience json.RawMessage `json:"aud"`
	Expiry   int64           `json:"exp"`
	Email    string          `json:"email"`
	Nonce    string          `json:"nonce"`
}

func (c *claims) expiry() time.Time { return time.Unix(c.Expiry, 0).UTC() }

func (c *claims) hasAudience(clientID string) bool {
	var one string
	if json.Unmarshal(c.Audience, &one) == nil {
		return one == clientID
	}
	var many []string
	return json.Unmarshal(c.Audience, &many) == nil && slices.Contains(many, clientID)
}

var errMalformedIDToken = errors.New("the provider returned a malformed ID token")

// parseClaims decodes a JWT's payload. Errors never quote the token.
func parseClaims(token string) (*claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errMalformedIDToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errMalformedIDToken
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil || c.Expiry == 0 {
		return nil, errMalformedIDToken
	}
	return &c, nil
}

// checkIDToken validates what the CLI can check without the provider's keys: the token is for
// this issuer and client, and (at login) answers this flow's nonce.
func checkIDToken(token, issuer, clientID, nonce string) (*claims, error) {
	c, err := parseClaims(token)
	if err != nil {
		return nil, err
	}
	if strings.TrimRight(c.Issuer, "/") != strings.TrimRight(issuer, "/") {
		return nil, fmt.Errorf("the ID token was issued by %q, not %s", c.Issuer, issuer)
	}
	if !c.hasAudience(clientID) {
		return nil, fmt.Errorf("the ID token is not for client %q", clientID)
	}
	if nonce != "" && c.Nonce != nonce {
		return nil, errors.New("the ID token does not answer this sign-in (nonce mismatch)")
	}
	return c, nil
}

// ConnectionError is a failure to reach the server or issuer at all (exit 4, D-15.4), as opposed
// to an answer that refused.
type ConnectionError struct {
	Target string
	Err    error
}

func (e *ConnectionError) Error() string { return fmt.Sprintf("cannot reach %s: %v", e.Target, e.Err) }
func (e *ConnectionError) Unwrap() error { return e.Err }
