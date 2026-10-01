package credentialstore

import (
	"bytes"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
)

type developmentIdentityProvider struct {
	path string
}

func newDevelopmentIdentityProvider(vaultPath string) identityProvider {
	return &developmentIdentityProvider{path: developmentIdentityPath(vaultPath)}
}

func (p *developmentIdentityProvider) Describe() string {
	return "development-only identity file " + p.path
}

func (p *developmentIdentityProvider) LoadOrCreate(vaultExists bool) (identityDocument, error) {
	raw, err := os.ReadFile(p.path)
	if err == nil {
		return decodeIdentityDocument(raw)
	}
	if !os.IsNotExist(err) {
		return identityDocument{}, fmt.Errorf("read development credential vault identity: %w", err)
	}
	if vaultExists {
		return identityDocument{}, fmt.Errorf("%w: development identity is missing", ErrVaultCorrupt)
	}
	doc, err := generateIdentityDocument()
	if err != nil {
		return identityDocument{}, err
	}
	data, err := encodeIdentityDocument(doc)
	if err != nil {
		return identityDocument{}, err
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(p.path), Source: bytes.NewReader(data),
		Mode: fileMode, DirMode: 0o700,
	}); err != nil {
		return identityDocument{}, err
	}
	return doc, nil
}

func (p *developmentIdentityProvider) Purge() error {
	if err := os.Remove(p.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
