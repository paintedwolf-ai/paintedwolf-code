package extpacks

import (
	"context"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/config"
)

var (
	activeMu    sync.RWMutex
	active      *EffectiveCatalog
	activeStamp string

	fallbackMu    sync.Mutex
	fallbackEff   *EffectiveCatalog
	fallbackStamp string
)

// SetActive installs the process-wide device catalog.
func SetActive(e *EffectiveCatalog) {
	setActiveWithStamp(e, DeviceDesiredStamp())
}

func setActiveWithStamp(e *EffectiveCatalog, stamp string) {
	activeMu.Lock()
	defer activeMu.Unlock()
	active = e
	activeStamp = stamp
}

// markActiveStamp records a handled device state.
func markActiveStamp(stamp string) {
	activeMu.Lock()
	defer activeMu.Unlock()
	activeStamp = stamp
}

// ReplaceActiveFrom swaps the matching catalog without changing its intent stamp.
func ReplaceActiveFrom(old, next *EffectiveCatalog) {
	if next == nil {
		return
	}
	activeMu.Lock()
	defer activeMu.Unlock()
	if active != old {
		return
	}
	active = next
}

// ClearActive removes the process-wide effective catalog.
func ClearActive() {
	activeMu.Lock()
	defer activeMu.Unlock()
	active = nil
	activeStamp = ""
}

// ActiveIsStale reports whether device intent changed after activation.
func ActiveIsStale() bool {
	activeMu.RLock()
	installed, stamp := active, activeStamp
	activeMu.RUnlock()
	if installed == nil {
		return false
	}
	return stamp != DeviceDesiredStamp()
}

var (
	refresherMu sync.Mutex
	refresher   *activeRefresher
)

type activeRefresher struct{ run func(context.Context) }

// SetActiveRefresher registers publication until its owner releases it.
func SetActiveRefresher(fn func(context.Context)) func() {
	registration := &activeRefresher{run: fn}
	refresherMu.Lock()
	refresher = registration
	refresherMu.Unlock()
	return func() {
		refresherMu.Lock()
		defer refresherMu.Unlock()
		if refresher == registration {
			refresher = nil
		}
		registration.run = nil
	}
}

// RefreshActiveIfStale republishes changed intent, retaining the catalog on failure.
func RefreshActiveIfStale(ctx context.Context) {
	if !ActiveIsStale() {
		return
	}
	refresherMu.Lock()
	var fn func(context.Context)
	if refresher != nil {
		fn = refresher.run
	}
	refresherMu.Unlock()
	if fn != nil {
		fn(ctx)
	}
}

// Active returns the process-wide effective catalog, or nil if unset.
func Active() *EffectiveCatalog {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return active
}

// CatalogForConsumers uses the active catalog or resolves committed device state.
func CatalogForConsumers() (*EffectiveCatalog, error) {
	if e := Active(); e != nil {
		return e, nil
	}
	// Path and source generation distinguish catalogs with identical file stamps.
	stampPath, _ := DeviceDesiredPath()
	stamp := fmt.Sprintf("%s|%s|%d", stampPath, DeviceDesiredStamp(), config.SourceGeneration())
	fallbackMu.Lock()
	defer fallbackMu.Unlock()
	if fallbackEff != nil && fallbackStamp == stamp {
		return fallbackEff, nil
	}
	eff, err := ResolveCatalog(context.Background(), nil, nil)
	if err != nil {
		return nil, err
	}
	fallbackEff = eff
	fallbackStamp = stamp
	return eff, nil
}
