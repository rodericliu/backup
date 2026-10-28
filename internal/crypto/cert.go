package crypto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// File names written by InitCA and IssueServerCert (PRD U-9).
const (
	CACertFile     = "ca.crt"
	CAKeyFile      = "ca.key"
	ServerCertFile = "server.crt"
	ServerKeyFile  = "server.key"
)

// Certificate lifetimes.
const (
	CAValidity     = 20 * 365 * 24 * time.Hour
	ServerValidity = 5 * 365 * 24 * time.Hour
)

// InitCA creates a self-signed CA in dir. It refuses to overwrite an
// existing CA.
func InitCA(dir, commonName string, now time.Time) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"backup"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create CA certificate: %w", err)
	}
	return writePair(dir, CACertFile, CAKeyFile, der, key)
}

// IssueServerCert signs a server certificate for hosts (DNS names or IP
// addresses) with the CA in caDir and writes it to outDir. Existing server
// files are replaced.
func IssueServerCert(caDir, outDir string, hosts []string, now time.Time) error {
	if len(hosts) == 0 {
		return errors.New("at least one host is required")
	}
	caCert, caKey, err := loadCA(caDir)
	if err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: hosts[0], Organization: []string{"backup"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(ServerValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if tmpl.NotAfter.After(caCert.NotAfter) {
		tmpl.NotAfter = caCert.NotAfter
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("create server certificate: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", outDir, err)
	}
	for _, f := range []string{ServerCertFile, ServerKeyFile} {
		if err := os.Remove(filepath.Join(outDir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return writePair(outDir, ServerCertFile, ServerKeyFile, der, key)
}

func loadCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		return nil, nil, fmt.Errorf("read CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, CAKeyFile))
	if err != nil {
		return nil, nil, fmt.Errorf("read CA key: %w", err)
	}
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, nil, errors.New("CA files are not PEM encoded")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA key: %w", err)
	}
	return cert, key, nil
}

// writePair writes a certificate (0644) and its key (0600), failing if
// either exists.
func writePair(dir, certName, keyName string, der []byte, key *ecdsa.PrivateKey) error {
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeExcl(filepath.Join(dir, keyName), 0o600, &pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}); err != nil {
		return err
	}
	return writeExcl(filepath.Join(dir, certName), 0o644, &pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeExcl(path string, mode os.FileMode, b *pem.Block) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return errors.Join(pem.Encode(f, b), f.Close())
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n
}
