package remote

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"atlas-commander/internal/atomicfile"
)

// loadOrCreateCert reads cert.pem and key.pem from dir, or makes a new
// self-signed ECDSA P-256 certificate valid for ten years. The phone pins the
// fingerprint, so the certificate must stay the same between runs: a new one
// would unpair every phone.
func loadOrCreateCert(dir string) (tls.Certificate, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", err
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && len(cert.Certificate) > 0 {
		if c, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil && time.Now().Before(c.NotAfter) {
			return cert, fingerprint(cert.Certificate[0]), nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Atlas Commander"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := atomicfile.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := atomicfile.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, fingerprint(der), nil
}

func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// loadOrCreateToken reads the token file, or makes a token if it is missing
// or empty.
func loadOrCreateToken(dir string) (string, error) {
	if b, err := os.ReadFile(filepath.Join(dir, "token")); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	}
	return newToken(dir)
}

// newToken writes 32 random bytes, base64url without padding, as the token.
func newToken(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	if tok == "" {
		return "", errors.New("empty token")
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, "token"), []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}
