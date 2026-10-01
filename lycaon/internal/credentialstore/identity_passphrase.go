package credentialstore

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"github.com/lycaon/lycaon/internal/fseffect"
)

const (
	minUnlockSecretBytes  = 12
	maxIdentityBytes      = 64 << 10
	maxIdentityCiphertext = maxIdentityBytes + (1 << 20)
)

type passphraseIdentityProvider struct {
	path string
}

func newPassphraseIdentityProvider(vaultPath string) identityProvider {
	return &passphraseIdentityProvider{path: identityPath(vaultPath)}
}

func (p *passphraseIdentityProvider) Describe() string { return "app password" }

func (p *passphraseIdentityProvider) LoadOrCreate(vaultExists bool) (identityDocument, error) {
	password := configuredUnlockSecret()
	defer clear(password)
	raw, err := readBoundedFile(p.path, maxIdentityCiphertext)
	if err != nil && !os.IsNotExist(err) {
		return identityDocument{}, fmt.Errorf("read wrapped credential vault identity: %w", err)
	}
	if os.IsNotExist(err) {
		if vaultExists {
			return identityDocument{}, fmt.Errorf("%w: wrapped identity is missing", ErrVaultCorrupt)
		}
		if len(password) == 0 {
			return identityDocument{}, ErrVaultUninitialized
		}
		if len(password) < minUnlockSecretBytes || strings.TrimSpace(string(password)) == "" {
			return identityDocument{}, fmt.Errorf("credential vault password must be at least %d bytes", minUnlockSecretBytes)
		}
		doc, createErr := generateIdentityDocument()
		if createErr != nil {
			return identityDocument{}, createErr
		}
		if createErr := p.commit(doc, password); createErr != nil {
			return identityDocument{}, createErr
		}
		return doc, nil
	}
	if len(password) == 0 {
		return identityDocument{}, ErrVaultLocked
	}
	identity, err := age.NewScryptIdentity(string(password))
	if err != nil {
		return identityDocument{}, fmt.Errorf("prepare credential vault password: %w", err)
	}
	reader, err := age.Decrypt(bytes.NewReader(raw), identity)
	if err != nil {
		return identityDocument{}, fmt.Errorf("%w: password is incorrect or the wrapped identity is damaged", ErrVaultUnlockFailed)
	}
	plaintext, err := io.ReadAll(io.LimitReader(reader, maxIdentityBytes+1))
	if err != nil {
		return identityDocument{}, fmt.Errorf("%w: read identity: %w", ErrVaultCorrupt, err)
	}
	if len(plaintext) > maxIdentityBytes {
		return identityDocument{}, fmt.Errorf("%w: identity exceeds %d bytes", ErrVaultCorrupt, maxIdentityBytes)
	}
	return decodeIdentityDocument(plaintext)
}

func (p *passphraseIdentityProvider) commit(doc identityDocument, password []byte) error {
	plaintext, err := encodeIdentityDocument(doc)
	if err != nil {
		return err
	}
	recipient, err := age.NewScryptRecipient(string(password))
	if err != nil {
		return fmt.Errorf("prepare credential vault password: %w", err)
	}
	var ciphertext bytes.Buffer
	writer, err := age.Encrypt(&ciphertext, recipient)
	if err != nil {
		return fmt.Errorf("wrap credential vault identity: %w", err)
	}
	if _, err := writer.Write(plaintext); err != nil {
		return fmt.Errorf("wrap credential vault identity payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish credential vault identity: %w", err)
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(p.path), Source: bytes.NewReader(ciphertext.Bytes()),
		Mode: fileMode, DirMode: 0o700,
	})
	return err
}

func (p *passphraseIdentityProvider) Purge() error {
	if err := os.Remove(p.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
