//go:build darwin

package gitcredential

import (
	"errors"
	"fmt"

	"github.com/keybase/go-keychain"
)

type keychainBackend struct{}

func newBackend() backend { return keychainBackend{} }

func (keychainBackend) Get(credential Credential) (Credential, bool, error) {
	query, err := internetPasswordItem(credential)
	if err != nil {
		return Credential{}, false, err
	}
	query.SetMatchLimit(keychain.MatchLimitOne)
	query.SetReturnAttributes(true)
	query.SetReturnData(true)
	results, err := keychain.QueryItem(query)
	if err != nil {
		return Credential{}, false, fmt.Errorf("read Git credential from macOS Keychain: %w", err)
	}
	if len(results) == 0 {
		return Credential{}, false, nil
	}
	return Credential{
		Protocol: credential.Protocol,
		Host:     credential.Host,
		Path:     credential.Path,
		Username: results[0].Account,
		Password: string(results[0].Data),
	}, true, nil
}

func (keychainBackend) Store(credential Credential) error {
	item, err := internetPasswordItem(credential)
	if err != nil {
		return err
	}
	item.SetData([]byte(credential.Password))
	item.SetLabel("Git credential for " + credential.Host)
	item.SetAccessible(keychain.AccessibleWhenUnlocked)
	if err := keychain.AddItem(item); err == nil {
		return nil
	} else if !errors.Is(err, keychain.ErrorDuplicateItem) {
		return fmt.Errorf("store Git credential in macOS Keychain: %w", err)
	}
	query, err := internetPasswordItem(credential)
	if err != nil {
		return err
	}
	update := keychain.NewItem()
	update.SetData([]byte(credential.Password))
	if err := keychain.UpdateItem(query, update); err != nil {
		return fmt.Errorf("update Git credential in macOS Keychain: %w", err)
	}
	return nil
}

func (keychainBackend) Erase(credential Credential) error {
	item, err := internetPasswordItem(credential)
	if err != nil {
		return err
	}
	if err := keychain.DeleteItem(item); err != nil && !errors.Is(err, keychain.ErrorItemNotFound) {
		return fmt.Errorf("erase Git credential from macOS Keychain: %w", err)
	}
	return nil
}

func internetPasswordItem(credential Credential) (keychain.Item, error) {
	protocol, err := keychainProtocol(credential.Protocol)
	if err != nil {
		return keychain.Item{}, err
	}
	host, port := splitHostPort(credential.Host)
	if host == "" {
		return keychain.Item{}, errors.New("credential host is required")
	}
	item := keychain.NewItem()
	item.SetSecClass(keychain.SecClassInternetPassword)
	item.SetProtocol(protocol)
	item.SetAuthenticationType("dflt")
	item.SetServer(host)
	item.SetPort(port)
	item.SetPath(credential.Path)
	item.SetAccount(credential.Username)
	return item, nil
}
