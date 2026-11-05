package recovery_test

import "github.com/lycaon/lycaon/internal/layeredfs/adapter/fuselinux"

func newMountedWorkflowHost() mountedWorkflowHost {
	return mountedWorkflowHost{
		adapter:      fuselinux.New(fuselinux.Config{}),
		quiesce:      fuselinux.NoopQuiesceHook,
		flushClients: fuselinux.NoWritableMappingsFlushHook,
	}
}
