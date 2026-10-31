// Package tlstest issues a throwaway certificate authority with server and
// client certificates for hermetic TLS and mutual-TLS tests.
package tlstest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// CA is a test certificate authority.
type CA struct {
	Cert *x509.Certificate
	key  *ecdsa.PrivateKey
	// PEM is the CA certificate as a trust bundle.
	PEM []byte
}

// Leaf is an issued certificate with its PEM chain and unencrypted PKCS#8 key.
type Leaf struct {
	TLS     tls.Certificate
	CertPEM []byte
	KeyPEM  []byte
	KeyDER  []byte
}

var serial atomic.Int64

func template(t testing.TB, cn string) *x509.Certificate {
	t.Helper()
	now := time.Now()
	return &x509.Certificate{SerialNumber: big.NewInt(serial.Add(1)), Subject: pkix.Name{CommonName: cn}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour)}
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// NewCA creates a self-signed authority named cn.
func NewCA(t testing.TB, cn string) *CA {
	t.Helper()
	key := newKey(t)
	tmpl := template(t, cn)
	tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
	tmpl.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	return &CA{Cert: cert, key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *CA) issue(t testing.TB, tmpl *x509.Certificate) Leaf {
	t.Helper()
	key := newKey(t)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("issue %s: %v", tmpl.Subject.CommonName, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("pair %s: %v", tmpl.Subject.CommonName, err)
	}
	return Leaf{TLS: pair, CertPEM: certPEM, KeyPEM: keyPEM, KeyDER: keyDER}
}

// Server issues a server certificate for DNS names and IP literals.
func (ca *CA) Server(t testing.TB, hosts ...string) Leaf {
	t.Helper()
	tmpl := template(t, hosts[0])
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, host)
		}
	}
	return ca.issue(t, tmpl)
}

// Client issues a client-authentication certificate.
func (ca *CA) Client(t testing.TB, cn string) Leaf {
	t.Helper()
	tmpl := template(t, cn)
	tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	return ca.issue(t, tmpl)
}

// Pool trusts only this authority.
func (ca *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// EncryptPKCS8 wraps a PKCS#8 key as PBES2 (PBKDF2-HMAC-SHA256, AES-256-CBC),
// the form `openssl pkey -aes256` writes.
func EncryptPKCS8(t testing.TB, keyDER []byte, passphrase string) []byte {
	t.Helper()
	salt, iv := make([]byte, 16), make([]byte, aes.BlockSize)
	_, _ = rand.Read(salt)
	_, _ = rand.Read(iv)
	const iterations = 2048
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iterations, 32)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	pad := aes.BlockSize - len(keyDER)%aes.BlockSize
	plain := append(append([]byte(nil), keyDER...), make([]byte, pad)...)
	for i := len(keyDER); i < len(plain); i++ {
		plain[i] = byte(pad)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	sealed := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(sealed, plain)
	type algorithm struct {
		OID    asn1.ObjectIdentifier
		Params asn1.RawValue `asn1:"optional"`
	}
	marshal := func(v any) asn1.RawValue {
		der, err := asn1.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return asn1.RawValue{FullBytes: der}
	}
	kdf := marshal(struct {
		Salt       []byte
		Iterations int
		KeyLength  int
		PRF        algorithm
	}{salt, iterations, 32, algorithm{OID: asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}, Params: asn1.NullRawValue}})
	params := marshal(struct{ KDF, Enc algorithm }{
		algorithm{OID: asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}, Params: kdf},
		algorithm{OID: asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}, Params: marshal(iv)},
	})
	der, err := asn1.Marshal(struct {
		Algorithm algorithm
		Data      []byte
	}{algorithm{OID: asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}, Params: params}, sealed})
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: der})
}
