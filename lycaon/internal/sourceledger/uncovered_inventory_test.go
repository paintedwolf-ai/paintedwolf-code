package sourceledger

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
)

// uncoveredInventory models lost watcher coverage after real disk observation.
type uncoveredInventory struct {
	commandsInventoryPort
}

func (i uncoveredInventory) EnsureInventory(ctx context.Context, request InventoryRequest) error {
	err := i.commandsInventoryPort.EnsureInventory(ctx, request)
	for _, root := range request.Roots {
		repochange.CloseRoot(root.Path)
		repochange.WaitQuiet(context.WithoutCancel(ctx), root.Path, repochange.DefaultDebounce, 5*time.Second)
	}
	return err
}

func loseInventoryCoverage(store *Store, root string) {
	store.Commands.inventory = uncoveredInventory{store.Commands.inventory}
	repochange.CloseRoot(root)
}
