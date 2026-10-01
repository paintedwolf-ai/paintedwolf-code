// Package credentialstore exposes validated namespaces in one encrypted vault.
package credentialstore

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// SecretValue wraps a sensitive secret string. String, LogValue,
// MarshalText, and MarshalJSON redact the content.
type SecretValue string

// Value returns the unredacted secret string.
func (s SecretValue) Value() string {
	return string(s)
}

// String returns "[REDACTED]" to prevent formatting leaks.
func (s SecretValue) String() string {
	return "[REDACTED]"
}

// LogValue returns "[REDACTED]" for slog structured logging.
func (s SecretValue) LogValue() slog.Value {
	return slog.StringValue("[REDACTED]")
}

// MarshalText returns "[REDACTED]" for text encoders.
func (s SecretValue) MarshalText() ([]byte, error) {
	return []byte("[REDACTED]"), nil
}

// MarshalJSON returns `"[REDACTED]"` for JSON encoders.
func (s SecretValue) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

// Validator accepts or rejects a credential slot id.
type Validator func(string) bool

// Store is a concurrency-safe private credential map over a Backend.
type Store struct {
	mu       sync.RWMutex
	backend  Backend
	keys     map[string]string
	validate Validator
	context  string
}

// IDs returns the stored slot identities without exposing their values.
func (s *Store) IDs() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.keys))
	for id := range s.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Open loads a slot with the platform identity provider.
func Open(slot Slot, validate Validator) (*Store, error) {
	backend, err := selectBackend(slot)
	if err != nil {
		return nil, err
	}
	return openWith(backend, slot.Context, validate)
}

// OpenFile loads a slot with a development identity file.
func OpenFile(slot Slot, validate Validator) (*Store, error) {
	return openWith(newDevelopmentBackend(slot.Path, slot.Namespace), slot.Context, validate)
}

func openWith(backend Backend, context string, validate Validator) (*Store, error) {
	s := &Store{
		backend: backend, keys: make(map[string]string),
		validate: validate, context: strings.TrimSpace(context),
	}
	loaded, err := backend.Snapshot()
	if err != nil {
		return nil, err
	}
	for id, key := range loaded.Values {
		id = strings.TrimSpace(id)
		if id == "" || key == "" {
			continue
		}
		if s.validate == nil || s.validate(id) {
			s.keys[id] = key
		}
	}
	return s, nil
}

// NewEmpty constructs an unloaded slot with a development identity file.
func NewEmpty(slot Slot, validate Validator) *Store {
	return &Store{
		backend: newDevelopmentBackend(slot.Path, slot.Namespace), keys: make(map[string]string),
		validate: validate, context: strings.TrimSpace(slot.Context),
	}
}

// Get returns a stored key wrapped in SecretValue.
func (s *Store) Get(id string) (SecretValue, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.keys[id]
	return SecretValue(value), ok
}

// Set stores a non-empty key in an accepted slot.
func (s *Store) Set(id, apiKey string) error {
	id = strings.TrimSpace(id)
	if id == "" || (s.validate != nil && !s.validate(id)) {
		return fmt.Errorf("unknown %s credential %q", s.context, id)
	}
	if apiKey == "" {
		return fmt.Errorf("%s credential value required", s.context)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = make(map[string]string)
	}
	previous, existed := s.keys[id]
	s.keys[id] = apiKey
	if err := s.backend.Set(id, apiKey); err != nil {
		if existed {
			s.keys[id] = previous
		} else {
			delete(s.keys, id)
		}
		return err
	}
	return nil
}

// Delete removes a key from an accepted slot.
func (s *Store) Delete(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || (s.validate != nil && !s.validate(id)) {
		return fmt.Errorf("unknown %s credential %q", s.context, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.keys[id]
	delete(s.keys, id)
	if err := s.backend.Delete(id); err != nil {
		if existed {
			s.keys[id] = previous
		}
		return err
	}
	return nil
}

// PurgeVault removes the encrypted vault and its identity.
func PurgeVault(path string) error {
	vault, err := selectVault(path)
	if err != nil {
		return err
	}
	return vault.purge()
}

// DescribeSlot names the storage protecting a slot without loading it.
func DescribeSlot(slot Slot) (string, error) {
	backend, err := selectBackend(slot)
	if err != nil {
		return "", err
	}
	return backend.Describe(), nil
}
