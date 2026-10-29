package contract

import (
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

func TestLinuxNativeSetupRestoresExactArchivesBeforeInstallation(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var action struct {
		Runs struct {
			Steps []struct {
				Name, Run, Uses, If, ID string
				With                    map[string]string
			}
		}
	}
	data := contractcheck.ReadRepoFile(t, root, ".github/actions/setup-linux-native/action.yml")
	contractcheck.FailErr(t, "decode native package setup", yaml.Unmarshal([]byte(data), &action))
	var phases []string
	for _, step := range action.Runs.Steps {
		switch {
		case strings.HasSuffix(strings.TrimSpace(step.Run), `provision.py" plan`):
			phases = append(phases, "plan")
		case strings.HasPrefix(step.Uses, "actions/cache/restore@"):
			phases = append(phases, "restore")
			if step.ID != "cache" {
				t.Error("archive restore must expose its hit result to publication")
			}
			if step.With["restore-keys"] != "" {
				t.Error("native package archives must restore only their exact resolved key")
			}
		case strings.HasSuffix(strings.TrimSpace(step.Run), `provision.py" install`):
			phases = append(phases, "install")
		case strings.HasPrefix(step.Uses, "actions/cache/save@"):
			phases = append(phases, "save")
			if step.If != "inputs.save == 'true' && github.ref == 'refs/heads/main' && steps.cache.outputs.cache-hit != 'true'" {
				t.Error("native archives publish only after successful trusted-main installation")
			}
		}
	}
	if !slices.Equal(phases, []string{"plan", "restore", "install", "save"}) {
		t.Fatalf("native package preparation phases %v", phases)
	}
	packages := strings.Fields(contractcheck.ReadRepoFile(t, root, ".github/actions/setup-linux-native/packages.txt"))
	for _, required := range []string{"libwebkit2gtk-4.1-dev", "libgtk-3-dev", "libsoup-3.0-dev", "xvfb",
		"libjavascriptcoregtk-4.1-dev", "librsvg2-dev", "patchelf", "sway", "wl-clipboard", "wtype", "xclip",
		"mesa-vulkan-drivers", "libvulkan1"} {
		if !slices.Contains(packages, required) {
			t.Errorf("native verification requires %s", required)
		}
	}
	data = contractcheck.ReadRepoFile(t, root, ".github/actions/setup-verification/action.yml")
	contractcheck.FailErr(t, "decode shared setup", yaml.Unmarshal([]byte(data), &action))
	for _, step := range action.Runs.Steps {
		if step.Uses == "./.github/actions/setup-linux-native" &&
			step.If == "inputs.lookup-only != 'true' && (runner.os == 'Linux' && (inputs.setup == 'shell' || inputs.linux-launch == 'true'))" && step.With["save"] == "${{ inputs.save-caches }}" {
			return
		}
	}
	t.Fatal("Linux shell and protected-launch setup must use the shared native package action")
}
