package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The artifact gate must run before anything is published, and must not be
// softened with `|| true` — a swallowed verification reads as proof.
func TestArtifactGateRunsBeforeAnyPublish(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	workflow := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")

	verifyAt := strings.Index(workflow, "Verify and stage platform artifacts")
	if verifyAt < 0 {
		t.Fatal(".github/workflows/release.yml has no platform artifact gate")
	}

	for _, publishStep := range []string{"Publish immutable release objects", "Activate release channel last"} {
		publishAt := strings.Index(workflow, publishStep)
		if publishAt < 0 {
			t.Fatalf("release.yml has no %q step", publishStep)
		}
		if publishAt < verifyAt {
			t.Fatalf("%q runs before bundle:verify: nothing may be published before the artifact is audited", publishStep)
		}
	}

	stageScript := contractcheck.ReadRepoFile(t, root, "scripts/release-stage-platform-artifacts.sh")
	if !strings.Contains(stageScript, "bundle:verify -- --app") || !strings.Contains(stageScript, "--dmg") {
		t.Fatal("the platform artifact gate never verifies with --dmg: the notarization staple lives on the .dmg, " +
			"which is what the user actually downloads")
	}
	// Linux is audited through the package tools: both families are extracted,
	// their manifests re-hashed, and their metadata compared to the release.
	for _, linuxProof := range []string{
		"dpkg-deb -x", "rpm2cpio", "linux-package-manifest.py\" verify-tree",
		"bundled engine sidecar", "audit_linux_tree \".deb\"", "audit_linux_tree \".rpm\"",
		"Linux stages exactly deb,rpm with no updater artifact",
	} {
		if !strings.Contains(stageScript, linuxProof) {
			t.Fatalf("the platform artifact gate lacks Linux structural proof %q", linuxProof)
		}
	}

	bundleScript := contractcheck.ReadRepoFile(t, root, "scripts/den-build-bundle.sh")
	if !strings.Contains(bundleScript, "verify-bundle.sh") {
		t.Fatal("scripts/den-build-bundle.sh no longer verifies the bundle it just built")
	}
	if strings.Contains(bundleScript, "verify-bundle.sh\" \"${VERIFY_ARGS[@]}\" || true") {
		t.Fatal("the bundle verification is softened with `|| true`: a swallowed verification reads as proof")
	}
}

// Linux ships as .deb and .rpm packages; no shipped surface may build, stage, or
// expect a portable image. Issue text and historical findings live elsewhere.
func TestNoShippedSurfaceNamesThePortableImageFormat(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	needles := []string{"AppImage", "appimage", "AppRun", "libfuse2"}
	skipDirs := map[string]bool{"node_modules": true, "target": true, ".git": true, "detection-packs": true, "testdata": true}
	var offenders []string
	for _, tree := range []string{"scripts", "packaging", "lycaon-den/src", "lycaon-den/src-tauri/src", "lycaon-den/src-tauri/tauri.conf.json", "lycaon-den/src-tauri/tauri.linux.conf.json", "docs", "Taskfile.yml", "README.md", ".github/workflows"} {
		start := filepath.Join(root, tree)
		err := filepath.WalkDir(start, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if skipDirs[entry.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, "artifact_gate_contract_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, needle := range needles {
				if strings.Contains(string(body), needle) {
					offenders = append(offenders, strings.TrimPrefix(path, root+"/")+": "+needle)
					break
				}
			}
			return nil
		})
		contractcheck.FailErr(t, "scan "+tree, err)
	}
	if len(offenders) != 0 {
		t.Fatalf("shipped surfaces still name the retired Linux image format:\n%s", strings.Join(offenders, "\n"))
	}
}
