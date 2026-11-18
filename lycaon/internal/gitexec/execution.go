package gitexec

import (
	"context"
	"os"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/hostproxy"
)

// execution resolves the installation and host transport resources for a launch.
// Launch authority and credential release remain independent of these resources.
type execution struct {
	binaryPath         func() (string, error)
	emptyHooksDir      func() (string, error)
	lfsPath            func() (string, error)
	executablePath     func() (string, error)
	hostConfig         hostConfigReader
	proxyConfiguration func() hostproxy.Configuration
	agentSocket        func(context.Context) string
	credentialService  func(context.Context, string) (credentialService, error)
}

func productionExecution() execution {
	return execution{
		binaryPath:         gitengine.BinaryPath,
		emptyHooksDir:      gitengine.EmptyHooksDir,
		lfsPath:            resolveLFSPath,
		executablePath:     os.Executable,
		hostConfig:         hostConfigResolver{},
		proxyConfiguration: hostproxy.Current,
		agentSocket:        defaultAgentSocket,
		credentialService:  startCredentialService,
	}
}
