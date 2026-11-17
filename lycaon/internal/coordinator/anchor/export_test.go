package anchor

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
)

// LoadRegistry loads inform bindings from bindingsDir.
func LoadRegistry(bindingsDir extpacks.Source, schemaDir string) (*Registry, error) {
	if !anchorcatalog.Loaded() {
		if err := anchorcatalog.InstallBundled(); err != nil {
			return nil, fmt.Errorf("anchor catalog required before LoadRegistry: %w", err)
		}
	}
	loader, err := oar.NewLoader(schemaDir)
	if err != nil {
		return nil, fmt.Errorf("oar loader: %w", err)
	}
	r := &Registry{
		byAnchor:       make(map[ID][]*Binding),
		informPrimary:  make(map[ID]*Binding),
		renderToAnchor: make(map[string]ID),
		loader:         loader,
	}
	if err := r.loadInformDir(bindingsDir); err != nil {
		return nil, err
	}
	return r, nil
}
