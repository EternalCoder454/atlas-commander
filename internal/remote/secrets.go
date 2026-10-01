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
//
// renewed is true when a certificate was already on disk but could not be
// used (corrupt or expired), so phones paired with the old one must pair again.
func loadOrCreateCert(dir string) (cert tls.Certificate, fp string, renewed bool, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", false, err
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if old, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && len(old.Certificate) > 0 {
		if c, perr := x509.ParseCertificate(old.Certificate[0]); perr == nil && time.Now().Before(c.NotAfter) {
			return old, fingerprint(old.Certificate[0]), false, nil
		}
	}
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	renewed = certErr == nil || keyErr == nil
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, "", false, err
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
		return tls.Certificate{}, "", false, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := atomicfile.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, "", false, err
	}
	if err := atomicfile.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, "", false, err
	}
	cert, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	return cert, fingerprint(der), renewed, nil
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

// certRenewedNotice is shown in Settings when the certificate had to be made
// again, because every phone pinned the old fingerprint.
const certRenewedNotice = "The certificate was renewed, so phones must pair again."

// tightenPerms makes the folder private and the key and token readable only
// by the user, in case an older build or a backup restore left them looser.
// Failures are ignored: the files still work, and on Windows modes mean little.
func tightenPerms(dir string) {
	_ = os.Chmod(dir, 0o700)
	for _, name := range []string{"key.pem", "token"} {
		_ = os.Chmod(filepath.Join(dir, name), 0o600)
	}
}
