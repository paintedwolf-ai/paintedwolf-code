package delegations

import (
	"fmt"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/osprocess"
)

// newWorkerRecovery opens the durable coordinator under the engine state root.
func newWorkerRecovery(stateRoot string, privateGit *privategit.Manager) (*recovery.Coordinator, error) {
	root := enginepaths.WorkerRecoveryRootUnder(stateRoot)
	store, err := checkpoint.NewStore(filepath.Join(root, "checkpoints"))
	if err != nil {
		return nil, fmt.Errorf("open worker checkpoint store: %w", err)
	}
	boot, _ := osprocess.BootID()
	config := recovery.CoordinatorConfig{
		RootDir:         filepath.Join(root, "state"),
		CheckpointStore: store,
		CurrentBootID:   boot,
		ProcessCtrl:     recovery.RecordedProcessController{},
		MountReconciler: recovery.ConnectionReconciler{Recoveries: platform.ConnectionRecoveries()},
	}
	if privateGit != nil {
		config.GitManager, config.GitStore = privateGit, privateGit.Store()
	}
	return recovery.NewCoordinator(config)
}
