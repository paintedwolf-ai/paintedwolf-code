package command

import (
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/tools"
)

func agentCommandLaunch(tctx tools.ToolContext, subject string, confinement *confine.Confinement) lycexec.LaunchPlan {
	launch := lycexec.AgentLaunch(lycexec.LaunchAgentCommand, subject, confinement)
	if tctx.Files.PackageExecution != nil {
		launch = launch.WithReducedEnvironment().WithExtraEnv(packageExecutionCacheEnv()...)
	}
	return launch
}

func packageExecutionCacheEnv() []string {
	home, _ := os.UserHomeDir()
	join := func(base, rel string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(base, rel)
	}
	cacheBase := os.Getenv("XDG_CACHE_HOME")
	if cacheBase == "" && home != "" {
		cacheBase = filepath.Join(home, ".cache")
	}
	if cacheBase == "" {
		return nil
	}
	return []string{
		"BUN_INSTALL_CACHE_DIR=" + join(cacheBase, "bun"),
		"npm_config_cache=" + join(cacheBase, "npm"),
		"CARGO_HOME=" + join(cacheBase, "cargo"),
		"PIP_CACHE_DIR=" + join(cacheBase, "pip"),
		"UV_CACHE_DIR=" + join(cacheBase, "uv"),
		"GOCACHE=" + join(cacheBase, "go-build"),
	}
}
