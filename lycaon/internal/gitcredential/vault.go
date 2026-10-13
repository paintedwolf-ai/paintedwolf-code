package gitcredential

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
)

const vaultContext = "git"

// DefaultSlot names the vault namespace holding Git HTTPS credentials.
func DefaultSlot() (credentialstore.Slot, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return credentialstore.Slot{
		Path:      credentialstore.DefaultVaultPath(dir),
		Namespace: credentialstore.NamespaceGitCredentials,
		Context:   vaultContext,
	}, nil
}

// vaultBackend keeps Git credentials in the encrypted vault, keyed by the
// remote they authenticate to. Every operation reopens the namespace so a
// value stored by another engine process is visible immediately.
type vaultBackend struct {
	open func() (*credentialstore.Store, error)
}

// VaultBackend opens the platform vault for each operation.
func VaultBackend() backend {
	return vaultBackend{open: func() (*credentialstore.Store, error) {
		slot, err := DefaultSlot()
		if err != nil {
			return nil, err
		}
		return credentialstore.Open(slot, nil)
	}}
}

type storedCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// credentialKeys lists lookup keys from most to least specific.
func credentialKeys(credential Credential) ([]string, error) {
	protocol := strings.ToLower(strings.TrimSpace(credential.Protocol))
	host := strings.ToLower(strings.TrimSpace(credential.Host))
	if protocol == "" || host == "" {
		return nil, errors.New("credential protocol and host are required")
	}
	base := protocol + "://" + host
	if path := strings.Trim(strings.TrimSpace(credential.Path), "/"); path != "" {
		return []string{base + "/" + path, base}, nil
	}
	return []string{base}, nil
}

func (b vaultBackend) Get(credential Credential) (Credential, bool, error) {
	keys, err := credentialKeys(credential)
	if err != nil {
		return Credential{}, false, err
	}
	store, err := b.open()
	if err != nil {
		return Credential{}, false, fmt.Errorf("open Git credential vault: %w", err)
	}
	for _, key := range keys {
		raw, ok := store.Get(key)
		if !ok {
			continue
		}
		var stored storedCredential
		if err := json.Unmarshal([]byte(raw.Value()), &stored); err != nil {
			return Credential{}, false, fmt.Errorf("decode Git credential for %s: %w", key, err)
		}
		if credential.Username != "" && credential.Username != stored.Username {
			continue
		}
		return Credential{
			Protocol: credential.Protocol,
			Host:     credential.Host,
			Path:     credential.Path,
			Username: stored.Username,
			Password: stored.Password,
		}, true, nil
	}
	return Credential{}, false, nil
}

func (b vaultBackend) Store(credential Credential) error {
	keys, err := credentialKeys(credential)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(storedCredential{Username: credential.Username, Password: credential.Password})
	if err != nil {
		return err
	}
	store, err := b.open()
	if err != nil {
		return fmt.Errorf("open Git credential vault: %w", err)
	}
	return store.Set(keys[0], string(encoded))
}

func (b vaultBackend) Erase(credential Credential) error {
	keys, err := credentialKeys(credential)
	if err != nil {
		return err
	}
	store, err := b.open()
	if err != nil {
		return fmt.Errorf("open Git credential vault: %w", err)
	}
	raw, ok := store.Get(keys[0])
	if !ok {
		return nil
	}
	var stored storedCredential
	if err := json.Unmarshal([]byte(raw.Value()), &stored); err == nil && credential.Username != "" && credential.Username != stored.Username {
		return nil
	}
	return store.Delete(keys[0])
}
