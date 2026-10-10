package anchor

import (
	"context"
	"sync"
)

// AnchorsForFunc resolves a session registry.
type AnchorsForFunc func(context.Context, string) *Registry

type resolverRegistration struct{ resolve AnchorsForFunc }

var (
	anchorsForMu sync.RWMutex
	anchorsFor   *resolverRegistration
)

// SetAnchorsFor installs a resolver until its owner releases it.
func SetAnchorsFor(fn AnchorsForFunc) func() {
	registration := &resolverRegistration{resolve: fn}
	anchorsForMu.Lock()
	anchorsFor = registration
	anchorsForMu.Unlock()
	return func() {
		anchorsForMu.Lock()
		defer anchorsForMu.Unlock()
		if anchorsFor == registration {
			anchorsFor = nil
		}
		registration.resolve = nil
	}
}
