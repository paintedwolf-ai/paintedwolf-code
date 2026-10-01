package secretmatch

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
)

const (
	fingerprintKeyBytes   = 32
	fingerprintKeyID      = "hmac-sha256-v1"
	fingerprintPrefix     = "sf1_"
	fingerprintDomain     = "painted-wolf/secret-fingerprint/v1\x00"
	fingerprintKeyContext = "secret fingerprint"
)

// SecretFingerprint is a host-only identity for exact secret bytes.
type SecretFingerprint string

// Fingerprinter derives stable identities with a device-held key.
type Fingerprinter struct {
	key [fingerprintKeyBytes]byte
}

// NewFingerprinter requires exactly 256 bits of key material.
func NewFingerprinter(key []byte) (*Fingerprinter, error) {
	if len(key) != fingerprintKeyBytes {
		return nil, fmt.Errorf("secret fingerprint key must be %d bytes", fingerprintKeyBytes)
	}
	f := &Fingerprinter{}
	copy(f.key[:], key)
	return f, nil
}

// DefaultFingerprintSlot returns the private device-key slot.
func DefaultFingerprintSlot() (credentialstore.Slot, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return credentialstore.Slot{
		Path:      credentialstore.DefaultVaultPath(dir),
		Namespace: credentialstore.NamespaceSecretFingerprint,
		Context:   fingerprintKeyContext,
	}, nil
}

// OpenFingerprinter loads or creates the device HMAC key.
func OpenFingerprinter() (*Fingerprinter, error) {
	slot, err := DefaultFingerprintSlot()
	if err != nil {
		return nil, err
	}
	store, err := credentialstore.Open(slot, validFingerprintKeyID)
	if err != nil {
		return nil, err
	}
	return loadOrCreateFingerprinter(store)
}

func validFingerprintKeyID(id string) bool { return id == fingerprintKeyID }

func loadOrCreateFingerprinter(store *credentialstore.Store) (*Fingerprinter, error) {
	if encoded, ok := store.Get(fingerprintKeyID); ok {
		key, err := base64.RawURLEncoding.DecodeString(encoded.Value())
		if err != nil {
			return nil, fmt.Errorf("decode secret fingerprint key: %w", err)
		}
		return NewFingerprinter(key)
	}
	key := make([]byte, fingerprintKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate secret fingerprint key: %w", err)
	}
	if err := store.Set(fingerprintKeyID, base64.RawURLEncoding.EncodeToString(key)); err != nil {
		return nil, fmt.Errorf("store secret fingerprint key: %w", err)
	}
	return NewFingerprinter(key)
}

// Fingerprint identifies exact bytes without normalization.
func (f *Fingerprinter) Fingerprint(secret string) SecretFingerprint {
	if f == nil || secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, f.key[:])
	_, _ = mac.Write([]byte(fingerprintDomain))
	_, _ = mac.Write([]byte(secret))
	return SecretFingerprint(fingerprintPrefix + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

// Fingerprints returns sorted unique identities from a match set.
func Fingerprints(matches []Match) []SecretFingerprint {
	seen := make(map[SecretFingerprint]struct{}, len(matches))
	for _, match := range matches {
		if match.Fingerprint != "" {
			seen[match.Fingerprint] = struct{}{}
		}
	}
	out := make([]SecretFingerprint, 0, len(seen))
	for fingerprint := range seen {
		out = append(out, fingerprint)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// FingerprintDigest is a stable opaque identity for an exact fingerprint set.
func FingerprintDigest(fingerprints []SecretFingerprint) string {
	seen := make(map[string]struct{}, len(fingerprints))
	for _, fingerprint := range fingerprints {
		if fingerprint != "" {
			seen[string(fingerprint)] = struct{}{}
		}
	}
	values := make([]string, 0, len(seen))
	for fingerprint := range seen {
		values = append(values, fingerprint)
	}
	sort.Strings(values)
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ScannerKey derives a separate key for the private scanner subprocess.
func (f *Fingerprinter) ScannerKey() []byte {
	if f == nil {
		return nil
	}
	mac := hmac.New(sha256.New, f.key[:])
	_, _ = mac.Write([]byte("painted-wolf/scanner-identities/v1"))
	return mac.Sum(nil)
}

// ScannerFingerprint uses the scanner domain without exposing the device key.
func (f *Fingerprinter) ScannerFingerprint(value string) SecretFingerprint {
	if f == nil {
		return ""
	}
	scanner, _ := NewFingerprinter(f.ScannerKey())
	return scanner.Fingerprint(value)
}
