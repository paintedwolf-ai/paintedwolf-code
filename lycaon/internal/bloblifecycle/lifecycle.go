// Package bloblifecycle orders retained-object deletion against publication and backup.
package bloblifecycle

import (
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
)

// Lifecycle serializes reclamation against publication through reference commit.
type Lifecycle struct{ sync.RWMutex }

var lifecycles [64]Lifecycle

// ForDevice coordinates stores under the same data root.
func ForDevice(dataDir string) *Lifecycle {
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	var key uint64
	for _, c := range []byte(fspath.CanonicalPath(dataDir)) {
		key = key*33 + uint64(c)
	}
	return &lifecycles[key%uint64(len(lifecycles))]
}

// AcquirePublication takes the lifecycle lock before the database write lock.
func AcquirePublication(dataDir string) func() {
	lifecycle := ForDevice(dataDir)
	lifecycle.RLock()
	return lifecycle.RUnlock
}
