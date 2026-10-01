package webresearch

import (
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
)

// CredentialStore validates stored identifiers against its catalog.
type CredentialStore struct {
	store   *credentialstore.Store
	catalog *Catalog
}

const credentialContext = "web research"

// DefaultSlot describes where web-research credentials live on this platform.
func DefaultSlot() (credentialstore.Slot, error) {
	path, err := userCredentialsPath()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return credentialstore.Slot{
		Path:      path,
		Namespace: credentialstore.NamespaceWebResearch,
		Context:   credentialContext,
	}, nil
}

// NewCredentialStore loads credentials from the platform's store, accepting the
// slots cat declares.
func NewCredentialStore(cat *Catalog) (*CredentialStore, error) {
	slot, err := DefaultSlot()
	if err != nil {
		return nil, err
	}
	return openCredentialStore(slot, cat)
}

// NewCredentialStoreAt opens an empty store at path with a development identity file.
func NewCredentialStoreAt(path string, cat *Catalog) *CredentialStore {
	wrapper := &CredentialStore{catalog: cat}
	wrapper.store = credentialstore.NewEmpty(credentialstore.Slot{
		Path: path, Namespace: credentialstore.NamespaceWebResearch, Context: credentialContext,
	}, wrapper.ValidID)
	return wrapper
}

func openCredentialStore(slot credentialstore.Slot, cat *Catalog) (*CredentialStore, error) {
	wrapper := &CredentialStore{catalog: cat}
	inner, err := credentialstore.Open(slot, wrapper.ValidID)
	if err != nil {
		return nil, err
	}
	wrapper.store = inner
	return wrapper, nil
}

func userCredentialsPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return credentialstore.DefaultVaultPath(dir), nil
}

// ValidID reports whether id is a credential slot this store's catalog declares.
func (s *CredentialStore) ValidID(id string) bool {
	if s == nil || s.catalog == nil {
		return false
	}
	for _, slot := range s.catalog.CredentialSlots() {
		if slot == id {
			return true
		}
	}
	return false
}

// Get returns the stored API key for id.
func (s *CredentialStore) Get(id string) (credentialstore.SecretValue, bool) {
	if s == nil || s.store == nil {
		return "", false
	}
	return s.store.Get(id)
}

// Set stores an API key for id.
func (s *CredentialStore) Set(id, apiKey string) error {
	if !s.ValidID(id) {
		return fmt.Errorf("unknown web research credential %q", id)
	}
	return s.store.Set(id, apiKey)
}

// Delete removes a stored key.
func (s *CredentialStore) Delete(id string) error {
	if !s.ValidID(id) {
		return fmt.Errorf("unknown web research credential %q", id)
	}
	return s.store.Delete(id)
}

// Configured reports whether a non-empty key is stored or available via a
// catalog-authorized environment variable.
func (s *CredentialStore) Configured(id string) bool {
	if key, ok := s.Get(id); ok && strings.TrimSpace(key.Value()) != "" {
		return true
	}
	entry, ok := s.catalog.byIDForSlot(id)
	if !ok || entry.APIKeyEnv == "" {
		return false
	}
	return strings.TrimSpace(os.Getenv(entry.APIKeyEnv)) != ""
}

func (s *CredentialStore) CredentialSource(id string) string {
	if key, ok := s.Get(id); ok && strings.TrimSpace(key.Value()) != "" {
		return "stored"
	}
	entry, ok := s.catalog.byIDForSlot(id)
	if ok && entry.APIKeyEnv != "" && strings.TrimSpace(os.Getenv(entry.APIKeyEnv)) != "" {
		return "environment"
	}
	return "none"
}

func (c *Catalog) byIDForSlot(slot string) (CatalogEntry, bool) {
	if c == nil {
		return CatalogEntry{}, false
	}
	for _, entry := range c.Entries() {
		if entry.CredentialSlot == slot || entry.OptionalCredentialSlot == slot {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}
