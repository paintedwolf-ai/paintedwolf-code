package native

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestAgentCommandLaunchInjectsPackageCacheEnv(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	tctx := tools.ToolContext{
		Files: tools.InvocationFiles{PackageExecution: &packageexec.Execution{
			Manager: "bun",
		}},
	}
	confinement := &confine.Confinement{Roots: []string{t.TempDir()}}
	launch := agentCommandLaunch(tctx, "test-command", confinement)

	if launch.Environment != lycexec.EnvironmentReduced {
		t.Fatalf("expected EnvironmentReduced, got %q", launch.Environment)
	}

	expectedKeys := []string{
		"BUN_INSTALL_CACHE_DIR=",
		"npm_config_cache=",
		"CARGO_HOME=",
		"PIP_CACHE_DIR=",
		"UV_CACHE_DIR=",
		"GOCACHE=",
	}

	cacheBase := os.Getenv("XDG_CACHE_HOME")
	if cacheBase == "" {
		cacheBase = filepath.Join(home, ".cache")
	}
	for _, key := range expectedKeys {
		found := false
		for _, env := range launch.ExtraEnv {
			if strings.HasPrefix(env, key) {
				found = true
				val := strings.TrimPrefix(env, key)
				if !strings.HasPrefix(val, cacheBase) {
					t.Fatalf("expected %s to point within %s, got %s", key, cacheBase, val)
				}
				break
			}
		}
		if !found {
			t.Fatalf("expected ExtraEnv to contain %s, got %v", key, launch.ExtraEnv)
		}
	}
}
