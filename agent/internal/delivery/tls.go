package delivery

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
)

// ClientTLS is the agent's side of mutual TLS to the backend's ingest listener (ADR-014 D-14.4).
//
// The agent presents its own certificate, signed by the ingest-client CA — the only CA that
// listener trusts — and verifies the backend's certificate against the server CA ONLY. The
// system trust store is deliberately not consulted: a certificate a public CA issued for the
// backend's name is not the backend, and nothing inside a cluster needs public trust.
func ClientTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("agent client certificate: %w", err)
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("server CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("server CA: no certificate found in " + caFile)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      roots,
		// Both ends are ours and both speak 1.3; the backend refuses anything older anyway.
		MinVersion: tls.VersionTLS13,
	}, nil
}

// WithTLS makes the client present `config` on every connection. The server name to verify is
// taken from the ingest URL's host, which is the backend Service's DNS name.
func (c *Client) WithTLS(config *tls.Config) *Client {
	c.httpClient.Transport = &http.Transport{
		TLSClientConfig:     config,
		ForceAttemptHTTP2:   false,
		MaxIdleConnsPerHost: 2,
	}
	return c
}
