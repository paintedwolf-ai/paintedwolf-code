package contractfixture

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

type BlockingInventoryService struct {
	Started chan struct{}
	Release chan struct{}
	Once    sync.Once
	CtxErr  chan error
}

func (f *BlockingInventoryService) EnsureInventory(ctx context.Context, _ sourceledger.InventoryRequest) error {
	f.Once.Do(func() { close(f.Started) })
	<-f.Release
	f.CtxErr <- ctx.Err()
	return nil
}

func (f *BlockingInventoryService) InventoryState(
	context.Context,
	string,
	sourcebranch.ID,
	int,
) (sourceledger.InventoryState, error) {
	return sourceledger.InventoryState{Phase: sourceledger.InventoryScanning}, nil
}

func (*BlockingInventoryService) SuspendInventory(context.Context, string) (func(), error) {
	return func() {}, nil
}
func (*BlockingInventoryService) ObservePaths(context.Context, string, []sourceledger.RootSpec, []sourceledger.PathRef) (int, error) {
	return 0, nil
}
