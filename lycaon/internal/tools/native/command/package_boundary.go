package command

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/tools"
)

func agentCommandLaunch(tctx tools.ToolContext, subject string, confinement *confine.Confinement, lease *confine.ActionLease, commandLine string) lycexec.LaunchPlan {
	launch := lycexec.AgentLaunch(lycexec.LaunchAgentCommand, subject, confinement)
	if confinement == nil && strings.TrimSpace(tctx.Host.SessionScratchDir) != "" {
		scratch := strings.TrimSpace(tctx.Host.SessionScratchDir)
		if c := fspath.CanonicalPath(scratch); c != "" {
			scratch = c
		}
		launch = launch.WithExtraEnv(lycexec.ScratchDirEnv+"="+scratch, "TMPDIR="+scratch, "TMP="+scratch, "TEMP="+scratch)
	}
	launch.CurrentAuthority = tools.AgentLaunchAuthority(tctx, confinement, lease, commandEgressIdentity(tctx, subject, commandLine))
	if tctx.Files.PackageExecution != nil {
		launch = launch.WithExtraEnv(packageExecutionCacheEnv()...)
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
