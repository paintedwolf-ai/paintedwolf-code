// Package anchortestsetup installs the bundled Anchor registry for test binaries.
package anchortestsetup

import (
	"fmt"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
)

// Install loads the bundled registry into process-global test state.
func Install() {
	registry, err := anchor.LoadRegistryFromConfigRoot()
	if err != nil {
		panic(fmt.Errorf("load bundled anchor registry: %w", err))
	}
	anchor.SetDefaultRegistry(registry)
}
