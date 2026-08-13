package agentconn

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
	"testing"
	"time"
)

func TestTLSCertificateReloaderSwapsCertificateForNewHandshakes(t *testing.T) {
	directory := t.TempDir()
	certificateFile := filepath.Join(directory, "listener.crt")
	privateKeyFile := filepath.Join(directory, "listener.key")
	writeTLSIdentity(t, certificateFile, privateKeyFile, "before.example.com", 1)

	reloader, err := NewTLSCertificateReloader(certificateFile, privateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := certificateDNSName(t, reloader); got != "before.example.com" {
		t.Fatalf("initial DNS name = %q", got)
	}

	writeTLSIdentity(t, certificateFile, privateKeyFile, "after.example.com", 2)
	if got := certificateDNSName(t, reloader); got != "before.example.com" {
		t.Fatalf("certificate changed before reload: %q", got)
	}
	if err := reloader.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := certificateDNSName(t, reloader); got != "after.example.com" {
		t.Fatalf("reloaded DNS name = %q", got)
	}
}

func certificateDNSName(t *testing.T, reloader *TLSCertificateReloader) string {
	t.Helper()
	identity, err := reloader.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return certificate.DNSNames[0]
}

func writeTLSIdentity(t *testing.T, certificateFile, privateKeyFile, dnsName string, serial int64) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: dnsName},
		DNSNames: []string{dnsName}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certificateFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
}
