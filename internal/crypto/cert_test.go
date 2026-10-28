package crypto

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCertificates(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca")
	out := filepath.Join(dir, "tls")
	now := time.Now()
	if err := InitCA(ca, "backup CA", now); err != nil {
		t.Fatal(err)
	}
	if err := InitCA(ca, "backup CA", now); err == nil {
		t.Error("InitCA overwrote an existing CA")
	}
	if err := IssueServerCert(ca, out, nil, now); err == nil {
		t.Error("issued a certificate without hosts")
	}
	hosts := []string{"backup-server.internal", "10.20.0.10"}
	// Issuing twice replaces the server certificate.
	for range 2 {
		if err := IssueServerCert(ca, out, hosts, now); err != nil {
			t.Fatal(err)
		}
	}

	for path, want := range map[string]os.FileMode{
		filepath.Join(ca, CAKeyFile):       0o600,
		filepath.Join(ca, CACertFile):      0o644,
		filepath.Join(out, ServerKeyFile):  0o600,
		filepath.Join(out, ServerCertFile): 0o644,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), want)
		}
	}

	pair, err := tls.LoadX509KeyPair(filepath.Join(out, ServerCertFile), filepath.Join(out, ServerKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(filepath.Join(ca, CACertFile))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA certificate not PEM")
	}
	leaf := pair.Leaf
	for _, name := range hosts {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: pool}); err != nil {
			t.Errorf("verify %s: %v", name, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "other.internal", Roots: pool}); err == nil {
		t.Error("certificate valid for a name not in its SAN")
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: hosts[0]}); err == nil {
		t.Error("certificate verified without the CA")
	}
}
