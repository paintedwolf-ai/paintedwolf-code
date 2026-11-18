package contract

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
