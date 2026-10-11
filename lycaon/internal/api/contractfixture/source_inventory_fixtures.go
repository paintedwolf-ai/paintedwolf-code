package contractfixture

import (
	"context"
	"errors"
	"sync"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/storageusage"
	"github.com/lycaon/lycaon/internal/visual"
)

type RecordingInventoryService struct {
	Mu       sync.Mutex
	Requests []sourceledger.InventoryRequest
}

func (f *RecordingInventoryService) EnsureInventory(
	_ context.Context,
	req sourceledger.InventoryRequest,
) error {
	f.Mu.Lock()
	f.Requests = append(f.Requests, req)
	f.Mu.Unlock()
	return nil
}

func (f *RecordingInventoryService) Snapshot() []sourceledger.InventoryRequest {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return append([]sourceledger.InventoryRequest(nil), f.Requests...)
}

type StorageUsageFailStore struct{ visual.Store }

func (*RecordingInventoryService) InventoryState(
	context.Context,
	string,
	sourcebranch.ID,
	int,
) (sourceledger.InventoryState, error) {
	return sourceledger.InventoryState{Phase: sourceledger.InventoryReady, Complete: true}, nil
}

func (StorageUsageFailStore) StorageUsage(context.Context, string) (storageusage.Usage, error) {
	return storageusage.Usage{}, errors.New("artifact usage unavailable")
}

func (*RecordingInventoryService) SuspendInventory(context.Context, string) (func(), error) {
	return func() {}, nil
}
func (*RecordingInventoryService) ObservePaths(context.Context, string, []sourceledger.RootSpec, []sourceledger.PathRef) (int, error) {
	return 0, nil
}
