package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/configlayout"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestBundledSidecarStagesEngineRootNotConfig verifies bundle inputs.
func TestBundledSidecarStagesEngineRootNotConfig(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sidecarRS := contractcheck.RustModuleSource(t, root, "lycaon-den/src-tauri/src/sidecar.rs")

	if strings.Contains(sidecarRS, config.EnvConfigRoot) {
		t.Fatalf("sidecar.rs sets %s — that is the development override for bundled config, not a bundle "+
			"concern; a release resolves config from the embed", config.EnvConfigRoot)
	}
	if !strings.Contains(sidecarRS, configlayout.EnvEngineRoot) {
		t.Fatalf("sidecar.rs must pass %s so staged payloads resolve", configlayout.EnvEngineRoot)
	}

	stageScript := readFile(t, filepath.Join(root, "scripts", "stage-engine.sh"))
	if strings.Contains(stageScript, `"${ENGINE_ROOT}/config/"`) {
		t.Fatal("stage-engine.sh stages lycaon/config into engine-root — the engine embeds it")
	}
	if !strings.Contains(stageScript, `"${ENGINE_ROOT}/`+configlayout.SchemasDirName+`/"`) {
		t.Fatalf("stage-engine.sh must stage %s/ into engine-root — it is not embedded and the engine "+
			"resolves it there at startup", configlayout.SchemasDirName)
	}
	if !strings.Contains(stageScript, `./cmd/pw-logs`) {
		t.Fatal("stage-engine.sh must build the pw-logs sibling beside pw")
	}

	tauriConf := readFile(t, filepath.Join(root, "lycaon-den", "src-tauri", "tauri.conf.json"))
	if !strings.Contains(tauriConf, `"binaries/pw-logs"`) {
		t.Fatal("tauri.conf.json must declare binaries/pw-logs as externalBin")
	}
}

// Boot paths that install the host anchor catalog / OAR profile must read the
// embed. InstallFile against moduleRoot works in a checkout and fails in the
// shipped .app (FindModuleRoot falls through to the literal "lycaon").
func TestBootInstallsBundledAnchorCatalog(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range []string{
		"lycaon/internal/catalogview/view.go",
		"lycaon/internal/app/build_oar.go",
	} {
		src := readFile(t, filepath.Join(root, rel))
		if strings.Contains(src, "anchorcatalog.InstallFile") {
			t.Fatalf("%s calls anchorcatalog.InstallFile — boot must use InstallBundled so a release without a checkout config tree can start", rel)
		}
		if !strings.Contains(src, "anchorcatalog.InstallBundled") {
			t.Fatalf("%s must call anchorcatalog.InstallBundled", rel)
		}
		if !strings.Contains(src, "InstallCapabilityBundled") {
			t.Fatalf("%s must call InstallCapabilityBundled", rel)
		}
	}
}

// The marker the shell probes must be a file staging actually produces. A config
// path would pass in a checkout and fail in the shipped .app, which stages
// payloads and no config at all.
func TestBundledEngineMarkerIsAStagedPayload(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sidecarRS := contractcheck.RustModuleSource(t, root, "lycaon-den/src-tauri/src/sidecar.rs")
	marker := filepath.Join(configlayout.SchemasDirName, "oar", "oar.schema.json")
	if !strings.Contains(sidecarRS, filepath.ToSlash(marker)) {
		t.Fatalf("sidecar.rs must probe %q to detect a staged engine root", marker)
	}
	if _, err := os.Stat(filepath.Join(root, marker)); err != nil {
		t.Fatalf("engine-root marker missing from the repo tree: %v", err)
	}
}
