package pkg

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

// SelfSignedCertificate generates a new key per installation, never a bundled secret.
func SelfSignedCertificate(hosts []string) (string, string, error) {
	if len(hosts) > 32 {
		return "", "", fmt.Errorf("too many certificate hosts")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Kaguya"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	seen := map[string]bool{}
	for _, host := range append([]string{"localhost", "127.0.0.1", "::1"}, hosts...) {
		if seen[host] {
			continue
		}
		seen[host] = true
		if ip := net.ParseIP(host); ip != nil {
			if !ip.IsUnspecified() {
				cert.IPAddresses = append(cert.IPAddresses, ip)
			}
			continue
		}
		if host == "" || len(host) > 253 {
			return "", "", fmt.Errorf("invalid certificate hostname")
		}
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return "", "", fmt.Errorf("invalid certificate hostname")
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return "", "", fmt.Errorf("invalid certificate hostname")
				}
			}
		}
		cert.DNSNames = append(cert.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})), nil
}

func ServerTLSConfig(certPEM, keyPEM string) (*tls.Config, error) {
	if len(certPEM) > 128*1024 || len(keyPEM) > 32*1024 {
		return nil, fmt.Errorf("TLS certificate or key exceeds size limit")
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("invalid TLS certificate/key pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("invalid TLS leaf certificate")
	}
	if now := time.Now(); now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, fmt.Errorf("TLS certificate is expired or not yet valid")
	}
	if len(leaf.DNSNames)+len(leaf.IPAddresses) == 0 {
		return nil, fmt.Errorf("TLS certificate must contain DNS or IP subject alternative names")
	}
	if leaf.KeyUsage != 0 && leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return nil, fmt.Errorf("TLS certificate must allow digital signatures")
	}
	allowed := len(leaf.ExtKeyUsage) == 0
	for _, usage := range leaf.ExtKeyUsage {
		allowed = allowed || usage == x509.ExtKeyUsageServerAuth || usage == x509.ExtKeyUsageAny
	}
	if !allowed {
		return nil, fmt.Errorf("TLS certificate must allow server authentication")
	}
	switch key := leaf.PublicKey.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() < 2048 {
			return nil, fmt.Errorf("TLS RSA key must be at least 2048 bits")
		}
	case *ecdsa.PublicKey:
		if key.Curve.Params().BitSize < 256 {
			return nil, fmt.Errorf("TLS ECDSA key must be at least 256 bits")
		}
	case ed25519.PublicKey:
	default:
		return nil, fmt.Errorf("unsupported TLS public key algorithm")
	}
	for _, der := range pair.Certificate {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("invalid TLS certificate chain")
		}
		switch cert.SignatureAlgorithm {
		case x509.MD2WithRSA, x509.MD5WithRSA, x509.SHA1WithRSA, x509.DSAWithSHA1, x509.DSAWithSHA256, x509.ECDSAWithSHA1:
			return nil, fmt.Errorf("weak TLS certificate signature algorithm")
		}
	}
	pair.Leaf = leaf
	return &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}, nil
}
