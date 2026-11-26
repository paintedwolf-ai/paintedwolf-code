package execution_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/app/execution"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/tools"
)

// TestAppExecutionBuildAutoDiscoversPortablePythonImage verifies Finding 1:
// with PW_PORTABLE_PYTHON_IMAGE_DIR UNSET, staging a verified image into the
// app's configdir/portable-python/image allows execution.Build to construct
// portablepython.Service automatically and execute `python` through the
// normal registered session tool.
func TestAppExecutionBuildAutoDiscoversPortablePythonImage(t *testing.T) {
	sourceImageDir := strings.TrimSpace(os.Getenv(portablepython.EnvImageDir))
	if sourceImageDir == "" {
		t.Skip("PW_PORTABLE_PYTHON_IMAGE_DIR not set; skipping app composition root auto-discovery test")
	}

	// Stage the verified image into an isolated LYCAON_CONFIG_DIR/portable-python/image
	// and explicitly UNSET PW_PORTABLE_PYTHON_IMAGE_DIR so discovery MUST use
	// standard installed app locations.
	cfgDir := t.TempDir()
	stagedParent := filepath.Join(cfgDir, "portable-python")
	testutil.FailErr(t, "mkdir staged parent", os.MkdirAll(stagedParent, 0o755))
	stagedImage := filepath.Join(stagedParent, "image")
	testutil.FailErr(t, "symlink verified image", os.Symlink(sourceImageDir, stagedImage))

	t.Setenv(configdir.EnvConfigDir, cfgDir)
	t.Setenv(portablepython.EnvImageDir, "")

	discovered, err := portablepython.DiscoverImageDir()
	testutil.FailErr(t, "discover staged image without env var", err)
	if discovered == "" {
		t.Fatal("DiscoverImageDir returned empty path")
	}

	catalog := extpackstest.MustLoadStockCatalog(t)
	rt, err := execution.Build(nil, "", catalog, nil, nil)
	testutil.FailErr(t, "execution.Build with auto-discovered Portable Python", err)

	raw, err := rt.Host.Registry.Run(context.Background(), "python", map[string]any{
		"code": "print(1)",
	}, tools.ToolContext{})
	testutil.FailErr(t, "execute registered python tool from app execution runtime", err)

	var resp portablepython.ToolResponse
	testutil.FailErr(t, "decode python tool response", json.Unmarshal([]byte(raw), &resp))
	if !resp.CleanCompletion || resp.ExitCode != 0 || strings.TrimSpace(resp.Stdout) != "1" {
		t.Fatalf("unexpected python tool response from app execution runtime: %+v", resp)
	}
	if resp.EvidenceSummary == nil || !resp.EvidenceSummary.Visible {
		t.Fatalf("expected non-nil visible EvidenceSummary, got %+v", resp.EvidenceSummary)
	}
}
