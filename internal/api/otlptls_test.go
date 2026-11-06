package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writePair writes a self-signed certificate for localhost with the given serial and returns its paths.
func writePair(t *testing.T, dir string, serial int64) (certPath, keyPath string, der []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err = x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	certPath, keyPath = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath, der
}

func TestOTLPTLSOffByDefaultAndHalfPairRefused(t *testing.T) {
	if cfg, err := (OTLPTLS{}).config(); cfg != nil || err != nil {
		t.Fatalf("no pair: config = %v, %v; want nil, nil", cfg, err)
	}
	if err := (OTLPTLS{CertFile: "a"}).check(); err == nil {
		t.Error("a certificate without a key was accepted")
	}
	if err := (OTLPTLS{KeyFile: "b"}).check(); err == nil {
		t.Error("a key without a certificate was accepted")
	}
	if _, err := (OTLPTLS{CertFile: "/nope.crt", KeyFile: "/nope.key"}).config(); err == nil {
		t.Error("a pair that cannot be read was accepted")
	}
}

func TestOTLPTLSServesAndReloadsTheRenewedPair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, der1 := writePair(t, dir, 1)
	cfg, err := OTLPTLS{CertFile: certPath, KeyFile: keyPath}.config()
	if err != nil || cfg == nil {
		t.Fatalf("config = %v, %v", cfg, err)
	}
	lis, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()

	served := func() []byte {
		conn, err := tls.Dial("tcp", lis.Addr().String(), &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.ConnectionState().PeerCertificates[0].Raw
	}
	if string(served()) != string(der1) {
		t.Fatal("the first handshake did not serve the first certificate")
	}

	// A renewal: the files change on disk; within the recheck interval the old pair is served, after it the new one.
	r := &certReloader{certFile: certPath, keyFile: keyPath}
	if c, err := r.get(nil); err != nil || string(c.Certificate[0]) != string(der1) {
		t.Fatalf("first load: %v", err)
	}
	_, _, der2 := writePair(t, dir, 2)
	future := time.Now().Add(time.Minute)
	if err := os.Chtimes(certPath, future, future); err != nil {
		t.Fatal(err)
	}
	if c, _ := r.get(nil); string(c.Certificate[0]) != string(der1) {
		t.Fatal("the pair was re-read inside the recheck interval")
	}
	r.mu.Lock()
	r.loaded = time.Time{}
	r.mu.Unlock()
	c, err := r.get(nil)
	if err != nil || string(c.Certificate[0]) != string(der2) {
		t.Fatalf("the renewed pair was not loaded: %v", err)
	}
	// A half-written renewal keeps the last good pair.
	if err := os.WriteFile(keyPath, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Minute)
	_ = os.Chtimes(certPath, later, later)
	r.mu.Lock()
	r.loaded = time.Time{}
	r.mu.Unlock()
	if c, err := r.get(nil); err != nil || string(c.Certificate[0]) != string(der2) {
		t.Fatalf("a bad renewal replaced the good pair: %v", err)
	}
}
