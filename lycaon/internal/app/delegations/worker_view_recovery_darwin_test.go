package delegations

import (
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
)

func newRealEngineHost() realEngineHost {
	return realEngineHost{
		adapter:          nfsdarwin.New(nfsdarwin.Config{}),
		quiesce:          nfsdarwin.NoopQuiesceHook,
		flushClients:     nfsdarwin.NoWritableMappingsFlushHook,
		requireAvailable: func(*testing.T) {},
		// A soft NFS mount answers only after its retries, so the test takes the process exit as proof.
		requireServerGone: func(*testing.T, string) {},
		detach:            func(mountPath string) { _ = exec.Command(nfsdarwin.DefaultUmountBin, "-f", mountPath).Run() },
	}
}
