package outboundhttp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1" //nolint:gosec // PBKDF2's default PRF in PKCS#5; it derives a key, it does not sign.
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"hash"
	"net/http"
)

// ClientTLS is one request's added trust and client identity. Roots extend
// the system roots for verification; nothing here disables verification.
type ClientTLS struct {
	Roots        *x509.CertPool
	Certificates []tls.Certificate
}

// TLSMaterialError is unusable trust or client-identity material, found
// before anything is sent.
type TLSMaterialError struct {
	// Client distinguishes a client certificate or key from a trust bundle.
	Client bool
	Reason string
}

func (e *TLSMaterialError) Error() string { return e.Reason }

func (c *ClientTLS) apply(transport *http.Transport) {
	if c == nil {
		return
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.Roots, Certificates: c.Certificates}
	if transport.TLSClientConfig != nil {
		config = transport.TLSClientConfig.Clone()
		config.RootCAs, config.Certificates = c.Roots, c.Certificates
	}
	transport.TLSClientConfig = config
}

// TrustPool is the system roots plus every certificate in bundles. A bundle
// without a certificate is unusable.
func TrustPool(bundles ...[]byte) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for _, bundle := range bundles {
		if !pool.AppendCertsFromPEM(bundle) {
			return nil, &TLSMaterialError{Reason: "no PEM certificate in the CA bundle"}
		}
	}
	return pool, nil
}

// ClientCertificate pairs a PEM certificate chain with its private key. The
// key may sit in the certificate file or in keyPEM, and may be PKCS#8
// encrypted with PBES2, which passphrase opens.
func ClientCertificate(certPEM, keyPEM []byte, passphrase string) (tls.Certificate, error) {
	if keyPEM == nil {
		keyPEM = certPEM
	}
	var chain []byte
	for rest := certPEM; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			chain = append(chain, pem.EncodeToMemory(block)...)
		}
	}
	if chain == nil {
		return tls.Certificate{}, &TLSMaterialError{Client: true, Reason: "no PEM certificate in the client certificate file"}
	}
	key, err := privateKeyPEM(keyPEM, passphrase)
	if err != nil {
		return tls.Certificate{}, err
	}
	pair, err := tls.X509KeyPair(chain, key)
	if err != nil {
		return tls.Certificate{}, &TLSMaterialError{Client: true, Reason: "the private key does not match the client certificate"}
	}
	return pair, nil
}

func privateKeyPEM(data []byte, passphrase string) ([]byte, error) {
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, &TLSMaterialError{Client: true, Reason: "no PEM private key for the client certificate"}
		}
		switch block.Type {
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
			if _, encrypted := block.Headers["Proc-Type"]; encrypted {
				return nil, &TLSMaterialError{Client: true, Reason: "legacy PEM encryption is not supported; use a PKCS#8 encrypted key"}
			}
			return pem.EncodeToMemory(block), nil
		case "ENCRYPTED PRIVATE KEY":
			if passphrase == "" {
				return nil, &TLSMaterialError{Client: true, Reason: "the private key is encrypted and no passphrase was given"}
			}
			der, err := decryptPKCS8(block.Bytes, passphrase)
			if err != nil {
				return nil, err
			}
			return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
		}
	}
}

var (
	oidPBES2      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidHMACSHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACSHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidHMACSHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 11}
	aesCBCKeySize = map[string]int{"2.16.840.1.101.3.4.1.2": 16, "2.16.840.1.101.3.4.1.22": 24, "2.16.840.1.101.3.4.1.42": 32}
)

// maxPBKDF2Iterations bounds the work a hostile key file can demand.
const maxPBKDF2Iterations = 1 << 24

type encryptedPrivateKeyInfo struct {
	Algorithm pkix.AlgorithmIdentifier
	Data      []byte
}

type pbes2Params struct {
	KeyDerivation pkix.AlgorithmIdentifier
	Encryption    pkix.AlgorithmIdentifier
}

type pbkdf2Params struct {
	Salt       []byte
	Iterations int
	KeyLength  int                      `asn1:"optional"`
	PRF        pkix.AlgorithmIdentifier `asn1:"optional"`
}

var errWrongPassphrase = &TLSMaterialError{Client: true, Reason: "the passphrase does not open the private key"}

// decryptPKCS8 opens a PBES2 (PBKDF2 with AES-CBC) EncryptedPrivateKeyInfo.
func decryptPKCS8(der []byte, passphrase string) ([]byte, error) {
	unsupported := &TLSMaterialError{Client: true, Reason: "the private key's encryption is not PBES2 with PBKDF2 and AES-CBC"}
	var info encryptedPrivateKeyInfo
	if _, err := asn1.Unmarshal(der, &info); err != nil || !info.Algorithm.Algorithm.Equal(oidPBES2) {
		return nil, unsupported
	}
	var params pbes2Params
	if _, err := asn1.Unmarshal(info.Algorithm.Parameters.FullBytes, &params); err != nil || !params.KeyDerivation.Algorithm.Equal(oidPBKDF2) {
		return nil, unsupported
	}
	var kdf pbkdf2Params
	if _, err := asn1.Unmarshal(params.KeyDerivation.Parameters.FullBytes, &kdf); err != nil || kdf.Iterations < 1 || kdf.Iterations > maxPBKDF2Iterations {
		return nil, unsupported
	}
	var prf func() hash.Hash
	switch {
	case len(kdf.PRF.Algorithm) == 0 || kdf.PRF.Algorithm.Equal(oidHMACSHA1):
		prf = sha1.New
	case kdf.PRF.Algorithm.Equal(oidHMACSHA256):
		prf = sha256.New
	case kdf.PRF.Algorithm.Equal(oidHMACSHA512):
		prf = sha512.New
	default:
		return nil, unsupported
	}
	size, ok := aesCBCKeySize[params.Encryption.Algorithm.String()]
	var iv []byte
	if !ok || (kdf.KeyLength != 0 && kdf.KeyLength != size) {
		return nil, unsupported
	}
	if _, err := asn1.Unmarshal(params.Encryption.Parameters.FullBytes, &iv); err != nil || len(iv) != aes.BlockSize {
		return nil, unsupported
	}
	if len(info.Data) == 0 || len(info.Data)%aes.BlockSize != 0 {
		return nil, unsupported
	}
	key, err := pbkdf2.Key(prf, passphrase, kdf.Salt, kdf.Iterations, size)
	if err != nil {
		return nil, fmt.Errorf("derive private key encryption key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(info.Data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, info.Data)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize {
		return nil, errWrongPassphrase
	}
	for _, b := range plain[len(plain)-pad:] {
		if int(b) != pad {
			return nil, errWrongPassphrase
		}
	}
	plain = plain[:len(plain)-pad]
	if _, err := x509.ParsePKCS8PrivateKey(plain); err != nil {
		return nil, errWrongPassphrase
	}
	return plain, nil
}
