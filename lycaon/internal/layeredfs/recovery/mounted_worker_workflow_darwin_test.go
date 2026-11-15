package recovery_test

import "github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"

func newMountedWorkflowHost() mountedWorkflowHost {
	return mountedWorkflowHost{
		adapter:      nfsdarwin.New(nfsdarwin.Config{}),
		quiesce:      nfsdarwin.NoopQuiesceHook,
		flushClients: nfsdarwin.NoWritableMappingsFlushHook,
	}
}
