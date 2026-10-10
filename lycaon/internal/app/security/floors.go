package security

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/detectionpack"
)

// LoadFloors loads key-material and credential-store paths.
func (b *Detections) LoadFloors() error {
	if err := b.installKeyMaterial(); err != nil {
		return err
	}
	return b.installCredentialStores()
}

// installKeyMaterial installs the key-material write floor.
func (b *Detections) installKeyMaterial() error {
	paths, err := detectionpack.BundledKeyMaterialPaths()
	if err != nil {
		return fmt.Errorf("key material catalogue: %w", err)
	}
	confine.SetKeyMaterialPathsSource(func() []string { return paths })
	return nil
}

func (b *Detections) installCredentialStores() error {
	bundled, err := detectionpack.BundledCredentialStorePaths()
	if err != nil {
		return fmt.Errorf("credential store catalogue: %w", err)
	}
	confine.SetCredentialStorePathsSource(func() []string {
		// Preserve bundled deny paths when overlays add paths or fail to load.
		current := b.current.Load()
		if current == nil {
			return bundled
		}
		merged := append([]string(nil), bundled...)
		return append(merged, current.matcher.CredentialStorePaths()...)
	})
	return nil
}
