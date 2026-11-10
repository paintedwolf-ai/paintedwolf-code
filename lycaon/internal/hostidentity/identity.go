// Package hostidentity owns the install's Ed25519 key, stored outside database backups.
package hostidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
)

// FileName is the retired standalone identity key's name in the config directory.
const FileName = "host-identity.pem"

const (
	pemBlockType        = "PRIVATE KEY"
	hostIdentitySlotID  = "ed25519-pkcs8"
	hostIdentityContext = "host identity"
)

// Identity is the public face of this host.
type Identity struct {
	HostID    string
	PublicKey ed25519.PublicKey
}

// EncodedPublicKey returns the standard base64 of the 32 public key bytes.
func (id Identity) EncodedPublicKey() string {
	return base64.StdEncoding.EncodeToString(id.PublicKey)
}

// DefaultSlot describes where the host identity key lives on this platform.
func DefaultSlot() (credentialstore.Slot, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return SlotForDir(dir), nil
}

// SlotForDir returns the host identity vault slot for a config directory.
func SlotForDir(configDir string) credentialstore.Slot {
	return credentialstore.Slot{
		Path:      credentialstore.DefaultVaultPath(configDir),
		Namespace: credentialstore.NamespaceHostIdentity,
		Context:   hostIdentityContext,
	}
}

// LoadOrCreate reads the host key from the encrypted credential vault in
// configDir, importing and removing a valid legacy host-identity.pem if present,
// or generating a new key when both supported sources are absent.
// Unreadable or corrupt keys stop startup.
func LoadOrCreate(configDir string) (Identity, error) {
	slot := SlotForDir(configDir)
	legacyPath := filepath.Join(configDir, FileName)
	var resolved Identity
	_, err := credentialstore.LoadOrInit(
		slot,
		hostIdentitySlotID,
		func(raw string) error {
			id, err := parsePEM(slot.Path, []byte(raw))
			if err != nil {
				return err
			}
			resolved = id
			return nil
		},
		func() (string, error) {
			data, err := os.ReadFile(legacyPath) // #nosec G304 -- fixed name under the engine config directory
			if err == nil {
				if _, err := parsePEM(legacyPath, data); err != nil {
					return "", err
				}
				return string(data), nil
			}
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
			return generatePEM()
		},
		func() error {
			if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove legacy %s: %w", legacyPath, err)
			}
			return nil
		},
	)
	if err != nil {
		return Identity{}, err
	}
	return resolved, nil
}

func parsePEM(source string, data []byte) (Identity, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != pemBlockType || len(rest) != 0 {
		return Identity{}, fmt.Errorf("%s: not a single %s block", source, pemBlockType)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return Identity{}, fmt.Errorf("%s: %w", source, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return Identity{}, fmt.Errorf("%s: key is not Ed25519", source)
	}
	public, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return Identity{}, fmt.Errorf("%s: public key is not Ed25519", source)
	}
	return Identity{HostID: DeriveHostID(public), PublicKey: public}, nil
}

func generatePEM() (string, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("encode key: %w", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: pemBlockType, Bytes: der})
	if len(encoded) == 0 {
		return "", errors.New("encode key: empty PEM block")
	}
	return string(encoded), nil
}

// DeriveHostID maps a public key to its RFC 9562 version 8 UUID.
func DeriveHostID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	var id uuid.UUID
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x80
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}
