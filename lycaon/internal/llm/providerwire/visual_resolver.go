package providerwire

import "sync"

type visualRegistration struct{ resolve VisualBytesResolver }

var (
	visualMu      sync.RWMutex
	visualCurrent *visualRegistration
)

// SetVisualBytesResolver binds artifact resolution until its owner releases it.
func SetVisualBytesResolver(fn VisualBytesResolver) func() {
	registration := &visualRegistration{resolve: fn}
	visualMu.Lock()
	visualCurrent = registration
	visualMu.Unlock()
	return func() {
		visualMu.Lock()
		defer visualMu.Unlock()
		if visualCurrent == registration {
			visualCurrent = nil
		}
		registration.resolve = nil
	}
}

func visualResolver() VisualBytesResolver {
	visualMu.RLock()
	defer visualMu.RUnlock()
	if visualCurrent == nil {
		return nil
	}
	return visualCurrent.resolve
}
