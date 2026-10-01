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

	"github.com/lycaon/lycaon/internal/fssync"
)

// FileName is the identity key's name in the config directory.
const FileName = "host-identity.pem"

const pemBlockType = "PRIVATE KEY"

// Identity is the public face of this host.
type Identity struct {
	HostID    string
	PublicKey ed25519.PublicKey
}

// EncodedPublicKey returns the standard base64 of the 32 public key bytes.
func (id Identity) EncodedPublicKey() string {
	return base64.StdEncoding.EncodeToString(id.PublicKey)
}

// LoadOrCreate reads the host key from configDir, creating it on first run.
// Unreadable keys stop startup.
func LoadOrCreate(configDir string) (Identity, error) {
	path := filepath.Join(configDir, FileName)
	id, err := load(path)
	if !errors.Is(err, os.ErrNotExist) {
		return id, err
	}
	if err := create(path); err != nil {
		return Identity{}, err
	}
	return load(path)
}

func load(path string) (Identity, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- fixed name under the engine config directory
	if err != nil {
		return Identity{}, err
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != pemBlockType || len(rest) != 0 {
		return Identity{}, fmt.Errorf("%s: not a single %s block", path, pemBlockType)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return Identity{}, fmt.Errorf("%s: %w", path, err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return Identity{}, fmt.Errorf("%s: key is not Ed25519", path)
	}
	public, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return Identity{}, fmt.Errorf("%s: public key is not Ed25519", path)
	}
	return Identity{HostID: DeriveHostID(public), PublicKey: public}, nil
}

// Exclusive publication preserves a key created by a concurrent process.
func create(path string) error {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode key: %w", err)
	}
	staged, err := os.CreateTemp(filepath.Dir(path), "."+FileName+".*")
	if err != nil {
		return fmt.Errorf("stage key: %w", err)
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()
	if err := writeKey(staged, der); err != nil {
		_ = staged.Close()
		return fmt.Errorf("stage key: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("stage key: %w", err)
	}
	if err := os.Link(stagedPath, path); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return nil
}

func writeKey(f *os.File, der []byte) error {
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if err := pem.Encode(f, &pem.Block{Type: pemBlockType, Bytes: der}); err != nil {
		return err
	}
	return fssync.File(f)
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
