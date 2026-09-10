package pkg

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTLS13OnlyAndCertificateVerification(t *testing.T) {
	cert, key, err := SelfSignedCertificate([]string{"agent.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	config, err := ServerTLSConfig(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "agent.example.com"} {
		if err := config.Certificates[0].Leaf.VerifyHostname(host); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.TLS.Version != tls.VersionTLS13 {
			t.Error("non-TLS-1.3 request reached application")
		}
		io.WriteString(w, "secure")
	}))
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(cert))
	for _, version := range []uint16{tls.VersionTLS13, tls.VersionTLS12} {
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: version, MaxVersion: version}}
		client := &http.Client{Transport: transport}
		defer transport.CloseIdleConnections()
		resp, err := client.Get(server.URL)
		if version == tls.VersionTLS12 {
			if err == nil {
				resp.Body.Close()
				t.Fatal("TLS 1.2 accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	resp, err := http.Get("http://" + server.Listener.Addr().String())
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("plaintext status=%d", resp.StatusCode)
		}
	}
	_, otherKey, _ := SelfSignedCertificate(nil)
	if _, err := ServerTLSConfig(cert, otherKey); err == nil {
		t.Fatal("mismatched key accepted")
	}
	for _, host := range []string{"*.example.com", "https://localhost", "evil\n.com"} {
		if _, _, err := SelfSignedCertificate([]string{host}); err == nil {
			t.Fatalf("accepted hostname %q", host)
		}
	}
}

func TestRejectExpiredAndWrongPurposeCertificate(t *testing.T) {
	certPEM, keyPEM, err := SelfSignedCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*x509.Certificate)
	}{
		{"expired", func(c *x509.Certificate) {
			c.NotBefore = time.Now().Add(-2 * time.Hour)
			c.NotAfter = time.Now().Add(-time.Hour)
		}},
		{"future", func(c *x509.Certificate) { c.NotBefore = time.Now().Add(time.Hour) }},
		{"client only", func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} }},
		{"no SAN", func(c *x509.Certificate) { c.DNSNames = nil; c.IPAddresses = nil }},
		{"no signing", func(c *x509.Certificate) { c.KeyUsage = x509.KeyUsageKeyEncipherment }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				t.Fatal(err)
			}
			tc.change(cert)
			der, err := x509.CreateCertificate(rand.Reader, cert, cert, cert.PublicKey, pair.PrivateKey)
			if err != nil {
				t.Fatal(err)
			}
			bad := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
			if _, err := ServerTLSConfig(bad, keyPEM); err == nil {
				t.Fatal("invalid certificate accepted")
			}
		})
	}
}
