// Package auth signs the CLI in to a topology server and hands the API client a bearer token.
//
// Sign-in is the OAuth 2.0 authorisation-code flow with PKCE S256 and a loopback redirect on one
// of three fixed ports (ADR-015 D-15.8). The OIDC ID token is the bearer credential; it is kept,
// with its refresh token, in one locked, owner-only file (D-15.11) and refreshed under that lock
// shortly before it expires (D-15.10). Tokens are never printed, logged or put in an error.
package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrInsecureTransport is returned when a credential would cross plain HTTP to a host that is not
// loopback. It mirrors the chart's --cookie-secure rule (D-15.14): a port-forward is loopback, a
// plain-HTTP ingress or issuer is not.
var ErrInsecureTransport = errors.New("refusing to send credentials over plain http")

// CheckSecretURL reports whether a token, code, verifier or refresh token may be sent to raw.
// https is always allowed (and verified); http only to localhost or a loopback IP.
func CheckSecretURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("%w to %s: use https, or a loopback address such as a port-forward",
			ErrInsecureTransport, u.Host)
	default:
		return fmt.Errorf("unsupported URL scheme %q in %q: use http or https", u.Scheme, raw)
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// NewHTTPClient returns the client used for the server, the issuer and its endpoints. caFile, if
// set, adds a PEM CA to the system pool for an https server or issuer with a private certificate.
// There is deliberately no way to turn certificate verification off (D-15.14).
//
// Every redirect is re-checked against CheckSecretURL, so a token request cannot be bounced from
// https or loopback to a plain-HTTP host.
func NewHTTPClient(caFile string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading --ca-file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("--ca-file %s holds no PEM certificate", caFile)
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return CheckSecretURL(req.URL.String())
		},
	}, nil
}

// NormalizeServer is the key a server is stored under: the URL without a trailing slash, so
// "http://localhost:8080/" and "http://localhost:8080" are one entry.
func NormalizeServer(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid server URL %q: want http(s)://host[:port]", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("invalid server URL %q: no user, query or fragment", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}
