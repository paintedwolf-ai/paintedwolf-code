package app

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
)

// wireCredentialFloors loads key-material and credential-store paths.
func (b sessionWiring) wireCredentialFloors() error {
	if err := b.wireKeyMaterial(); err != nil {
		return err
	}
	return b.wireCredentialStores()
}

// wireKeyMaterial installs the key-material write floor.
func (b sessionWiring) wireKeyMaterial() error {
	paths, err := detectionpack.BundledKeyMaterialPaths()
	if err != nil {
		return fmt.Errorf("key material catalogue: %w", err)
	}
	confine.SetKeyMaterialPathsSource(func() []string { return paths })
	return nil
}

func (b sessionWiring) wireCredentialStores() error {
	bundled, err := detectionpack.BundledCredentialStorePaths()
	if err != nil {
		return fmt.Errorf("credential store catalogue: %w", err)
	}
	confine.SetCredentialStorePathsSource(func() []string {
		// Preserve bundled deny paths when overlays add paths or fail to load.
		current := b.detections.current.Load()
		if current == nil {
			return bundled
		}
		merged := append([]string(nil), bundled...)
		return append(merged, current.matcher.CredentialStorePaths()...)
	})
	return nil
}
