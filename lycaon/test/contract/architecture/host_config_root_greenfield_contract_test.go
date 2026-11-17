package contract

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestHostConfigRootMirrorsAcrossSurfaces(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)

	brand := contractcheck.ReadRepoFile(t, root, filepath.Join("lycaon-den", "shared", "brand.ts"))
	for _, needle := range []string{
		`CONFIG_DIR_NAME = "paintedwolf"`,
		`CONFIG_DIR_NAME_DEV = "paintedwolf-dev"`,
	} {
		if !strings.Contains(brand, needle) {
			t.Fatalf("shared/brand.ts missing %s", needle)
		}
	}

	rust := contractcheck.ReadRepoFile(t, root, filepath.Join("lycaon-den", "src-tauri", "src", "config_dir.rs"))
	for _, needle := range []string{
		`DIR_NAME_PROD: &str = "paintedwolf"`,
		`DIR_NAME_DEV: &str = "paintedwolf-dev"`,
	} {
		if !strings.Contains(rust, needle) {
			t.Fatalf("config_dir.rs missing %s", needle)
		}
	}

	scripts := contractcheck.ReadRepoFile(t, root, filepath.Join("scripts", "config-dir.sh"))
	for _, needle := range []string{
		`paintedwolf`,
		`paintedwolf-dev`,
		`lycaon_config_dir()`,
		`lycaon_channel_config_dir()`,
		`lycaon_overlay_dir()`,
		`OVERLAY_FIXTURE_DIR`,
		testutil.OverlayFixtureDirName,
	} {
		if !strings.Contains(scripts, needle) {
			t.Fatalf("scripts/config-dir.sh missing %s", needle)
		}
	}
}

// Shell project paths are shared while host storage stays channel-specific.
func TestShellOverlaySharedAcrossChannels(t *testing.T) {
	script := filepath.Join(contractcheck.RepoRoot(t), "scripts", "config-dir.sh")
	t.Setenv("CONFIG_DIR_NAME_PROD", configdir.DirNameProd)
	t.Setenv("CONFIG_DIR_NAME_DEV", configdir.DirNameDev)
	for _, tc := range []struct {
		dev  string
		host string
	}{
		{dev: "0", host: configdir.DirNameProd},
		{dev: "1", host: configdir.DirNameDev},
	} {
		t.Run("dev="+tc.dev, func(t *testing.T) {
			t.Setenv(configdir.EnvDev, tc.dev)
			cmd := exec.CommandContext(t.Context(), "bash", "-c", `source "$1"
lycaon_overlay_dir
lycaon_config_dir_leaf`, "overlay-paths", script)
			output, err := cmd.CombinedOutput()
			contractcheck.FailErr(t, "resolve shell overlay and host paths", err)
			want := settingsoverlay.DirName() + "\n" + tc.host + "\n"
			if string(output) != want {
				t.Fatalf("shell paths = %q, want %q", output, want)
			}
		})
	}
}
