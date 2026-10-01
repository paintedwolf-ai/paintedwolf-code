package webresearch

import (
	"context"
	"sync"
)

const (
	seedCallMaxParallelShared    = 2
	seedCallMaxParallelDedicated = 4
)

var (
	seedSemMu sync.Mutex
	seedSems  = map[string]chan struct{}{}
)

func seedCallParallelLimit(sharedWithCoordinator bool) int {
	if sharedWithCoordinator {
		return seedCallMaxParallelShared
	}
	return seedCallMaxParallelDedicated
}

func seedSemKey(providerID string, sharedWithCoordinator bool) string {
	if providerID == "" {
		providerID = "_unknown"
	}
	if sharedWithCoordinator {
		return providerID + ":shared"
	}
	return providerID + ":dedicated"
}

func seedCallSemFor(providerID string, sharedWithCoordinator bool) chan struct{} {
	key := seedSemKey(providerID, sharedWithCoordinator)
	seedSemMu.Lock()
	defer seedSemMu.Unlock()
	if sem, ok := seedSems[key]; ok {
		return sem
	}
	sem := make(chan struct{}, seedCallParallelLimit(sharedWithCoordinator))
	seedSems[key] = sem
	return sem
}

func acquireSeedCallSlot(ctx context.Context, providerID string, sharedWithCoordinator bool) error {
	sem := seedCallSemFor(providerID, sharedWithCoordinator)
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSeedCallSlot(providerID string, sharedWithCoordinator bool) {
	sem := seedCallSemFor(providerID, sharedWithCoordinator)
	select {
	case <-sem:
	default:
		wrlog.Warn("unbalanced seed-call slot release")
	}
}

func resetSeedCallSems() {
	seedSemMu.Lock()
	seedSems = map[string]chan struct{}{}
	seedSemMu.Unlock()
}
