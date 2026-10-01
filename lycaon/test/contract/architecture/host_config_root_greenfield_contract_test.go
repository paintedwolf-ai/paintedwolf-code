package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Host settings use the platform configuration directory.
func TestHostConfigRootNoHomeOverlayConstructors(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	codeRoot := filepath.Join(root, "lycaon")
	exts := map[string]struct{}{".go": {}}
	forbidden := []string{
		`filepath.Join(home, settingsoverlay.DirName()`,
		`filepath.Join(homedir, settingsoverlay.DirName()`,
		`filepath.Join(homeDir, settingsoverlay.DirName()`,
		`Join(home, settingsoverlay.DirName()`,
		`Join(homedir, settingsoverlay.DirName()`,
		`"$HOME/.paintedwolf`,
		`"${HOME}/.paintedwolf`,
		`"$HOME/.paintedwolf-dev`,
		`"${HOME}/.paintedwolf-dev`,
		`"$HOME/.lycaon`,
		`"${HOME}/.lycaon`,
		`"$HOME/.paintedwolfcode`,
		`"${HOME}/.paintedwolfcode`,
		`"$HOME/.paintedwolfcode-dev`,
		`"${HOME}/.paintedwolfcode-dev`,
		`"~/` + settingsoverlay.DirName() + `/`,
		`'~/` + settingsoverlay.DirName() + `/`,
	}
	var violations []string
	err := contractcheck.WalkFiles(codeRoot, exts, true, func(path string, data []byte) error {
		if strings.Contains(path, string(filepath.Join("test", "contract"))) {
			return nil
		}
		text := string(data)
		for _, needle := range forbidden {
			if strings.Contains(text, needle) {
				violations = append(violations, path+": contains "+needle)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk Go for $HOME/overlay host constructors", err)
	contractcheck.FailViolations(t, "production Go constructs host paths under $HOME/<overlay> (use configdir.UserConfigDir)", violations)
}

func TestHostConfigRootLeafSSOT(t *testing.T) {
	t.Parallel()
	if configdir.DirNameProd != "paintedwolf" {
		t.Fatalf("DirNameProd = %q", configdir.DirNameProd)
	}
	if configdir.DirNameDev != "paintedwolf-dev" {
		t.Fatalf("DirNameDev = %q", configdir.DirNameDev)
	}
	if configdir.LabelProd() != "~/.config/paintedwolf" {
		t.Fatalf("LabelProd = %q", configdir.LabelProd())
	}
}

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

// Corpus scripts share overlay path resolution.
func TestUpgradeCorpusScriptsUseOverlayHelper(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, name := range []string{"upgrade-corpus-seed.sh", "upgrade-corpus-boot.sh"} {
		text := contractcheck.ReadRepoFile(t, root, filepath.Join("scripts", name))
		if !strings.Contains(text, "config-dir.sh") {
			t.Fatalf("%s must source scripts/config-dir.sh", name)
		}
		if !strings.Contains(text, "lycaon_overlay_dir") {
			t.Fatalf("%s must resolve the overlay directory via lycaon_overlay_dir", name)
		}
		for _, bad := range []string{
			"/.paintedwolf",
			"/." + configdir.DirNameProd,
			"/." + configdir.DirNameDev,
		} {
			if strings.Contains(text, bad) {
				t.Fatalf("%s hardcodes overlay directory %q", name, bad)
			}
		}
	}
}

func TestHostConfigRootScriptsUseHelper(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scripts := filepath.Join(root, "scripts")
	mustSource := []string{
		"den-dev-sidecar.sh",
		"den-dev-app.sh",
		"den-fresh-session-state.sh",
		"db-wipe.sh",
		"debug-common.sh",
	}
	for _, name := range mustSource {
		text := contractcheck.ReadRepoFile(t, root, filepath.Join("scripts", name))
		if !strings.Contains(text, "config-dir.sh") {
			t.Fatalf("%s must source scripts/config-dir.sh", name)
		}
		if strings.Contains(text, `${HOME}/.config/paintedwolf"`) ||
			strings.Contains(text, `${HOME}/.config/lycaon`) {
			t.Fatalf("%s hardcodes a config path instead of lycaon_config_dir", name)
		}
	}
	if _, err := os.Stat(filepath.Join(scripts, "config-dir.sh")); err != nil {
		t.Fatalf("scripts/config-dir.sh missing: %v", err)
	}
}

func TestE2ESidecarIsolatesCanonicalConfigFiles(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	script := contractcheck.ReadRepoFile(t, root, filepath.Join("scripts", "e2e-sidecar-serve.sh"))
	for _, name := range []string{"credential-vault.age", "providers.local.yaml", settingsoverlay.BasenameModelPolicy} {
		if !strings.Contains(script, name) {
			t.Fatalf("e2e-sidecar-serve.sh does not copy %s", name)
		}
	}
	if strings.Contains(script, "model_policy.yaml") {
		t.Fatal("e2e-sidecar-serve.sh uses noncanonical model_policy.yaml spelling")
	}
	if !strings.Contains(script, `CONFIG_DIR="${STATE_DIR}/config"`) ||
		!strings.Contains(script, `export LYCAON_CONFIG_DIR="${CONFIG_DIR}"`) {
		t.Fatal("e2e-sidecar-serve.sh must pin device writes beneath its throwaway state")
	}
}

func TestDenConfigDirBrandContract(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lib := contractcheck.ReadRepoFile(t, root, filepath.Join("lycaon-den", "src-tauri", "src", "lib.rs"))
	if !strings.Contains(lib, "mod config_dir;") {
		t.Fatal("lib.rs must declare mod config_dir")
	}
	sidecar := contractcheck.RustModuleSource(t, root, "lycaon-den/src-tauri/src/sidecar.rs")
	if !strings.Contains(sidecar, `LYCAON_CONFIG_DIR`) {
		t.Fatal("sidecar spawn must pin LYCAON_CONFIG_DIR to the shell's resolved dir")
	}
}
