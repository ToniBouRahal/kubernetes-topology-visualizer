package delivery

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A throwaway CA that can issue server and client certificates, written to disk as the chart
// mounts them.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *testCA) issue(t *testing.T, name string, usage x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	cert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func writeKeyPair(t *testing.T, dir string, cert tls.Certificate) (string, string) {
	t.Helper()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	keyDER, _ := x509.MarshalECPrivateKey(cert.PrivateKey.(*ecdsa.PrivateKey))
	must(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0o600))
	must(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certFile, keyFile
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// An ingest listener as the backend runs it: TLS 1.3, a client certificate required, trusted
// only from the ingest-client CA.
func mtlsBackend(t *testing.T, server tls.Certificate, clientCA *testCA, seen *int) *httptest.Server {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(clientCA.cert)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen++
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{server},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func deliverOnce(t *testing.T, url string, config *tls.Config) error {
	t.Helper()
	c := New(url, 4, slog.New(slog.NewTextHandler(io.Discard, nil))).WithTLS(config)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.post(ctx, batch(id(1)))
	return err
}

// T-14.3 and the agent's half of T-14.1.
func TestMutualTLS(t *testing.T) {
	serverCA, ingestCA, otherCA := newCA(t, "server"), newCA(t, "ingest-client"), newCA(t, "other")
	backendCert := serverCA.issue(t, "127.0.0.1", x509.ExtKeyUsageServerAuth)
	agentCert := ingestCA.issue(t, "topology-agent", x509.ExtKeyUsageClientAuth)

	dir := t.TempDir()
	certFile, keyFile := writeKeyPair(t, dir, agentCert)
	caFile := filepath.Join(dir, "server-ca.crt")
	must(t, os.WriteFile(caFile, serverCA.pem, 0o600))

	t.Run("delivers to the real backend with its client certificate", func(t *testing.T) {
		seen := 0
		backend := mtlsBackend(t, backendCert, ingestCA, &seen)
		config, err := ClientTLS(certFile, keyFile, caFile)
		must(t, err)
		if err := deliverOnce(t, backend.URL, config); err != nil {
			t.Fatalf("delivery failed: %v", err)
		}
		if seen != 1 {
			t.Fatalf("backend saw %d requests, want 1", seen)
		}
	})

	t.Run("refuses a backend whose certificate another CA signed", func(t *testing.T) {
		seen := 0
		impostor := mtlsBackend(t, otherCA.issue(t, "127.0.0.1", x509.ExtKeyUsageServerAuth), ingestCA, &seen)
		config, err := ClientTLS(certFile, keyFile, caFile)
		must(t, err)
		if err := deliverOnce(t, impostor.URL, config); err == nil {
			t.Fatal("delivered a batch to a backend the server CA never signed")
		}
		if seen != 0 {
			t.Fatalf("impostor received %d batches", seen)
		}
	})

	t.Run("without a client certificate the backend refuses it", func(t *testing.T) {
		seen := 0
		backend := mtlsBackend(t, backendCert, ingestCA, &seen)
		config, err := ClientTLS(certFile, keyFile, caFile)
		must(t, err)
		config.Certificates = nil
		if err := deliverOnce(t, backend.URL, config); err == nil {
			t.Fatal("backend accepted a batch from a client with no certificate")
		}
		if seen != 0 {
			t.Fatalf("backend handled %d requests", seen)
		}
	})
}

func TestClientTLSRejectsMissingFiles(t *testing.T) {
	if _, err := ClientTLS("/nonexistent.crt", "/nonexistent.key", "/nonexistent-ca.crt"); err == nil {
		t.Fatal("expected an error for missing certificate files")
	}
}
