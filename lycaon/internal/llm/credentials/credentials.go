// Package credentials stores provider credentials independently of model discovery and request execution.
package credentials

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
)

// Store manages provider secrets.
type Store struct {
	store *credentialstore.Store
}

const credentialContext = "provider"

// DefaultSlot describes where provider credentials live on this platform.
func DefaultSlot() (credentialstore.Slot, error) {
	path, err := userCredentialsPath()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return credentialstore.Slot{
		Path:      path,
		Namespace: credentialstore.NamespaceProviders,
		Context:   credentialContext,
	}, nil
}

// New loads credentials from the platform's store.
func New() (*Store, error) {
	slot, err := DefaultSlot()
	if err != nil {
		return nil, err
	}
	store, err := credentialstore.Open(slot, nil)
	if err != nil {
		return nil, err
	}
	return &Store{store: store}, nil
}

// NewAt opens an empty store at path with a development identity file.
func NewAt(path string) *Store {
	return &Store{store: credentialstore.NewEmpty(credentialstore.Slot{
		Path: path, Namespace: credentialstore.NamespaceProviders, Context: credentialContext,
	}, nil)}
}

// Get returns the stored API key for providerID.
func (s *Store) Get(providerID string) (credentialstore.SecretValue, bool) {
	if s == nil || s.store == nil {
		return "", false
	}
	return s.store.Get(providerID)
}

// Set stores an API key for providerID.
func (s *Store) Set(providerID, apiKey string) error {
	providerID = strings.TrimSpace(providerID)
	apiKey = strings.TrimSpace(apiKey)
	if providerID == "" {
		return fmt.Errorf("provider id required")
	}
	if apiKey == "" {
		return fmt.Errorf("api key required")
	}
	return s.store.Set(providerID, apiKey)
}

// Delete removes a stored key.
func (s *Store) Delete(providerID string) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Delete(providerID)
}
func userCredentialsPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return credentialstore.DefaultVaultPath(dir), nil
}
