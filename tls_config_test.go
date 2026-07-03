package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/vim25/soap"
)

// newSoapClient returns a throwaway soap.Client for exercising configureTLSFromEnv
// without touching the network.
func newSoapClient(t *testing.T) *soap.Client {
	t.Helper()
	u, err := soap.ParseURL("https://vc.example.invalid")
	if err != nil {
		t.Fatalf("ParseURL: %v", err)
	}
	return soap.NewClient(u, false)
}

// writePEMCert writes a self-signed certificate PEM to a temp file and returns its path.
func writePEMCert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "esx-hcl-check test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// TestConfigureTLSFromEnv_NoEnv is the default path: no GOVC_TLS_* vars means no
// custom RootCAs (nil => Go verifies against the system trust store).
func TestConfigureTLSFromEnv_NoEnv(t *testing.T) {
	t.Setenv("GOVC_TLS_CA_CERTS", "")
	t.Setenv("GOVC_TLS_KNOWN_HOSTS", "")
	t.Setenv("GOVC_TLS_HANDSHAKE_TIMEOUT", "")

	sc := newSoapClient(t)
	if err := configureTLSFromEnv(sc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool := sc.DefaultTransport().TLSClientConfig.RootCAs; pool != nil {
		t.Errorf("RootCAs should stay nil (system roots) when GOVC_TLS_CA_CERTS is unset, got %v", pool)
	}
}

func TestConfigureTLSFromEnv_ValidCACerts(t *testing.T) {
	t.Setenv("GOVC_TLS_CA_CERTS", writePEMCert(t))

	sc := newSoapClient(t)
	if err := configureTLSFromEnv(sc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.DefaultTransport().TLSClientConfig.RootCAs == nil {
		t.Error("RootCAs should be populated from GOVC_TLS_CA_CERTS")
	}
}

func TestConfigureTLSFromEnv_MissingCACerts(t *testing.T) {
	t.Setenv("GOVC_TLS_CA_CERTS", filepath.Join(t.TempDir(), "does-not-exist.pem"))

	err := configureTLSFromEnv(newSoapClient(t))
	if err == nil || !strings.Contains(err.Error(), "GOVC_TLS_CA_CERTS") {
		t.Fatalf("expected a GOVC_TLS_CA_CERTS error, got %v", err)
	}
}

func TestConfigureTLSFromEnv_InvalidCACerts(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOVC_TLS_CA_CERTS", bad)

	err := configureTLSFromEnv(newSoapClient(t))
	if err == nil || !strings.Contains(err.Error(), "GOVC_TLS_CA_CERTS") {
		t.Fatalf("expected a GOVC_TLS_CA_CERTS error for a non-PEM file, got %v", err)
	}
}

func TestConfigureTLSFromEnv_KnownHosts(t *testing.T) {
	kh := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(kh, []byte("vc.example.com AA:BB:CC:DD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOVC_TLS_KNOWN_HOSTS", kh)

	sc := newSoapClient(t)
	if err := configureTLSFromEnv(sc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// soap.Client keys thumbprints by host:port (443 default), matched via Thumbprint.
	if got := sc.Thumbprint("vc.example.com"); got != "AA:BB:CC:DD" {
		t.Errorf("thumbprint not loaded: got %q, want %q", got, "AA:BB:CC:DD")
	}
}

// TestConfigureTLSFromEnv_MissingKnownHosts documents govmomi's behavior: a
// non-existent known_hosts path is a no-op, not an error.
func TestConfigureTLSFromEnv_MissingKnownHosts(t *testing.T) {
	t.Setenv("GOVC_TLS_KNOWN_HOSTS", filepath.Join(t.TempDir(), "missing"))
	if err := configureTLSFromEnv(newSoapClient(t)); err != nil {
		t.Fatalf("missing known_hosts should be a no-op, got %v", err)
	}
}

func TestConfigureTLSFromEnv_HandshakeTimeout(t *testing.T) {
	t.Setenv("GOVC_TLS_HANDSHAKE_TIMEOUT", "7s")

	sc := newSoapClient(t)
	if err := configureTLSFromEnv(sc); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := sc.DefaultTransport().TLSHandshakeTimeout; got != 7*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 7s", got)
	}
}

func TestConfigureTLSFromEnv_InvalidHandshakeTimeout(t *testing.T) {
	t.Setenv("GOVC_TLS_HANDSHAKE_TIMEOUT", "soon")

	err := configureTLSFromEnv(newSoapClient(t))
	if err == nil || !strings.Contains(err.Error(), "GOVC_TLS_HANDSHAKE_TIMEOUT") {
		t.Fatalf("expected a GOVC_TLS_HANDSHAKE_TIMEOUT error, got %v", err)
	}
}
