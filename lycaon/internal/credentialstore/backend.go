package credentialstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/filelock"
	"github.com/lycaon/lycaon/internal/fseffect"
)

const fileMode = 0o600

const (
	vaultFormat    = 1
	identityFormat = 1
	// MaxPlaintextBytes bounds both vault reads and committed plaintext.
	MaxPlaintextBytes  = 64 << 20
	maxVaultCiphertext = MaxPlaintextBytes + (1 << 20)
)

var ErrVaultCorrupt = errors.New("credential vault is damaged")

// Snapshot is one namespace's decrypted credential map.
type Snapshot struct {
	Values map[string]string
}

// Backend persists one credential namespace.
type Backend interface {
	Snapshot() (Snapshot, error)
	Set(id, secret string) error
	Delete(id string) error
	Describe() string
}

type identityDocument struct {
	Format   int    `json:"format"`
	VaultID  string `json:"vault_id"`
	Identity string `json:"identity"`
}

type vaultDocument struct {
	Format     int                          `json:"format"`
	VaultID    string                       `json:"vault_id"`
	Namespaces map[string]map[string]string `json:"namespaces"`
}

type identityProvider interface {
	LoadOrCreate(vaultExists bool) (identityDocument, error)
	Purge() error
	Describe() string
}

type vaultFile struct {
	mu       sync.Mutex
	path     string
	provider identityProvider
	identity *age.X25519Identity
	vaultID  string
}

type namespaceBackend struct {
	vault     *vaultFile
	namespace string
}

var vaults = struct {
	sync.Mutex
	byPath map[string]*vaultFile
}{byPath: make(map[string]*vaultFile)}

func sharedVault(path string, provider identityProvider) *vaultFile {
	clean := filepath.Clean(path)
	vaults.Lock()
	defer vaults.Unlock()
	if existing := vaults.byPath[clean]; existing != nil {
		return existing
	}
	created := &vaultFile{path: clean, provider: provider}
	vaults.byPath[clean] = created
	return created
}

func newNamespaceBackend(path, namespace string, provider identityProvider) Backend {
	return &namespaceBackend{
		vault: sharedVault(path, provider), namespace: strings.TrimSpace(namespace),
	}
}

func (b *namespaceBackend) Describe() string {
	return fmt.Sprintf("age-encrypted vault %s (%s)", b.vault.path, b.vault.provider.Describe())
}

func (b *namespaceBackend) Snapshot() (Snapshot, error) {
	var snapshot Snapshot
	err := b.vault.withExclusiveLock(func() error {
		doc, err := b.vault.read()
		if err != nil {
			return err
		}
		snapshot.Values = cloneValues(doc.Namespaces[b.namespace])
		return nil
	})
	return snapshot, err
}

func (b *namespaceBackend) Set(id, secret string) error {
	return b.mutate(func(values map[string]string) { values[id] = secret })
}

func (b *namespaceBackend) Delete(id string) error {
	return b.mutate(func(values map[string]string) { delete(values, id) })
}

func (b *namespaceBackend) mutate(apply func(map[string]string)) error {
	return b.vault.withExclusiveLock(func() error {
		doc, err := b.vault.read()
		if err != nil {
			return err
		}
		values := doc.Namespaces[b.namespace]
		if values == nil {
			values = make(map[string]string)
			doc.Namespaces[b.namespace] = values
		}
		apply(values)
		if len(values) == 0 {
			delete(doc.Namespaces, b.namespace)
		}
		return b.vault.commit(doc)
	})
}

func (v *vaultFile) ensureIdentity(vaultExists bool) error {
	if v.identity != nil {
		return nil
	}
	doc, err := v.provider.LoadOrCreate(vaultExists)
	if err != nil {
		return err
	}
	if doc.Format != identityFormat || strings.TrimSpace(doc.VaultID) == "" {
		return fmt.Errorf("%w: unsupported identity document", ErrVaultCorrupt)
	}
	if _, err := uuid.Parse(doc.VaultID); err != nil {
		return fmt.Errorf("%w: invalid vault identity", ErrVaultCorrupt)
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(doc.Identity))
	if err != nil {
		return fmt.Errorf("%w: parse vault identity: %w", ErrVaultCorrupt, err)
	}
	v.identity, v.vaultID = identity, doc.VaultID
	return nil
}

func (v *vaultFile) read() (vaultDocument, error) {
	raw, err := readBoundedFile(v.path, maxVaultCiphertext)
	if err != nil && !os.IsNotExist(err) {
		return vaultDocument{}, fmt.Errorf("read credential vault: %w", err)
	}
	exists := err == nil
	if err := v.ensureIdentity(exists); err != nil {
		return vaultDocument{}, err
	}
	if !exists {
		return vaultDocument{Format: vaultFormat, VaultID: v.vaultID, Namespaces: map[string]map[string]string{}}, nil
	}
	reader, err := age.Decrypt(bytes.NewReader(raw), v.identity)
	if err != nil {
		return vaultDocument{}, fmt.Errorf("%w: decrypt: %w", ErrVaultCorrupt, err)
	}
	plaintext, err := io.ReadAll(io.LimitReader(reader, MaxPlaintextBytes+1))
	if err != nil {
		return vaultDocument{}, fmt.Errorf("%w: read plaintext: %w", ErrVaultCorrupt, err)
	}
	if len(plaintext) > MaxPlaintextBytes {
		return vaultDocument{}, fmt.Errorf("%w: plaintext exceeds %d bytes", ErrVaultCorrupt, MaxPlaintextBytes)
	}
	var doc vaultDocument
	if err := json.Unmarshal(plaintext, &doc); err != nil {
		return vaultDocument{}, fmt.Errorf("%w: parse plaintext: %w", ErrVaultCorrupt, err)
	}
	if doc.Format != vaultFormat || doc.VaultID != v.vaultID || doc.Namespaces == nil {
		return vaultDocument{}, fmt.Errorf("%w: identity or format mismatch", ErrVaultCorrupt)
	}
	return doc, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), limit)
	}
	return io.ReadAll(io.LimitReader(file, limit+1))
}

func (v *vaultFile) commit(doc vaultDocument) error {
	plaintext, err := json.Marshal(doc) // #nosec G117 -- age encrypts this payload before it is written.
	if err != nil {
		return fmt.Errorf("encode credential vault: %w", err)
	}
	if len(plaintext) > MaxPlaintextBytes {
		return fmt.Errorf("credential vault exceeds %d-byte limit", MaxPlaintextBytes)
	}
	var ciphertext bytes.Buffer
	writer, err := age.Encrypt(&ciphertext, v.identity.Recipient())
	if err != nil {
		return fmt.Errorf("encrypt credential vault: %w", err)
	}
	if _, err := writer.Write(plaintext); err != nil {
		return fmt.Errorf("encrypt credential vault payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish credential vault encryption: %w", err)
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(v.path), Source: bytes.NewReader(ciphertext.Bytes()),
		Mode: fileMode, DirMode: 0o700,
	})
	return err
}

func (v *vaultFile) purge() error {
	return v.withExclusiveLock(func() error {
		// The custodian confirms first: an identity it cannot be reached to
		// forget would outlive the vault, and a vault whose custodian is merely
		// unreachable is never destroyed.
		if err := v.provider.Purge(); err != nil {
			return err
		}
		for _, path := range []string{v.path, developmentIdentityPath(v.path)} {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		v.identity, v.vaultID = nil, ""
		return nil
	})
}

func (v *vaultFile) withExclusiveLock(fn func() error) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	lock, err := filelock.Open(v.path + ".lock")
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		locked, lockErr := filelock.TryExclusive(lock)
		if lockErr != nil {
			return fmt.Errorf("lock credential vault: %w", lockErr)
		}
		if locked {
			callErr := fn()
			unlockErr := filelock.Unlock(lock)
			if callErr != nil {
				return callErr
			}
			if unlockErr != nil {
				return fmt.Errorf("unlock credential vault: %w", unlockErr)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("lock credential vault: timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func cloneValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for id, secret := range values {
		out[id] = secret
	}
	return out
}
