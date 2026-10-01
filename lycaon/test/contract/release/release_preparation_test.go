package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestReleasePreparationPreservesSchemaInputs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		version    string
		seed       string
		baselines  string
		revision   int
		wantReject bool
	}{
		{name: "first candidate", version: "1.0.0-rc.1", seed: "1.0.0-rc.1", baselines: "[]\n", revision: 1},
		{name: "later candidate", version: "2.0.0-rc.1", seed: "2.0.0-rc.1", revision: 2,
			baselines: `[{"revision":1,"shape":"` + strings.Repeat("a", 64) + `"}]`},
		{name: "wrong version", version: "1.0.0-rc.1", seed: "0.9.0", baselines: "[]\n", revision: 1, wantReject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			preparation, inputs := releasePreparationFixture(t, tc.version, tc.seed, tc.baselines, tc.revision)
			root := preparation.root
			out, err := preparation.run(t)
			destination := filepath.Join(root, "lycaon", "testdata", "upgrade-corpus", tc.version)
			if tc.wantReject {
				if err == nil {
					t.Fatalf("mismatched candidate succeeded: %s", out)
				}
				if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
					t.Fatalf("rejected preparation left an output: %v", statErr)
				}
			} else {
				if err != nil {
					t.Fatalf("prepare candidate: %v\n%s", err, out)
				}
				manifest := filepath.Join(destination, "MANIFEST.json")
				before, readErr := os.ReadFile(manifest)
				contractcheck.FailErr(t, "read prepared manifest", readErr)
				if out, repeatErr := preparation.run(t); repeatErr == nil {
					t.Fatalf("existing candidate was overwritten: %s", out)
				}
				after, readErr := os.ReadFile(manifest)
				contractcheck.FailErr(t, "read preserved manifest", readErr)
				if !bytes.Equal(before, after) {
					t.Fatal("refused replacement changed the candidate")
				}
			}
			for path, want := range inputs {
				got, readErr := os.ReadFile(filepath.Join(root, path))
				contractcheck.FailErr(t, "read preparation input", readErr)
				if !bytes.Equal(got, want) {
					t.Errorf("release preparation changed %s", path)
				}
			}
			staging, globErr := filepath.Glob(filepath.Join(filepath.Dir(destination), ".prepare.*"))
			contractcheck.FailErr(t, "inspect preparation staging", globErr)
			if len(staging) != 0 {
				t.Errorf("preparation left staging directories: %v", staging)
			}
		})
	}
}

func TestReleasePreparationPreservesDestinationSymlink(t *testing.T) {
	preparation, _ := releasePreparationFixture(t, "1.0.0-rc.1", "1.0.0-rc.1", "[]\n", 1)
	destination := filepath.Join(preparation.root, "lycaon", "testdata", "upgrade-corpus", "1.0.0-rc.1")
	target := filepath.Join(t.TempDir(), "absent")
	contractcheck.FailErr(t, "create corpus parent", os.MkdirAll(filepath.Dir(destination), 0o700))
	contractcheck.FailErr(t, "create destination symlink", os.Symlink(target, destination))
	out, err := preparation.run(t)
	if err == nil {
		t.Fatalf("preparation accepted an existing symlink: %s", out)
	}
	got, err := os.Readlink(destination)
	contractcheck.FailErr(t, "read preserved symlink", err)
	if got != target {
		t.Fatalf("destination points to %q, want %q", got, target)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("preparation changed the symlink target: %v", err)
	}
}

type releasePreparation struct {
	root  string
	paths contractcheck.ArtifactPaths
}

func (p releasePreparation) run(t *testing.T) ([]byte, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(p.root, "scripts", "upgrade-corpus-prepare.sh"))
	cmd.Env = append(os.Environ(), p.paths.Env()...)
	return cmd.CombinedOutput()
}

func releasePreparationFixture(t *testing.T, version, seedVersion, baselines string, revision int) (releasePreparation, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	paths := contractcheck.InstallArtifactPaths(t, root, t.TempDir())
	schemaLock, err := json.Marshal(map[string]int{"schema_version": revision})
	contractcheck.FailErr(t, "encode schema lock", err)
	inputs := map[string][]byte{
		"VERSION":                       []byte(version + "\n"),
		"lycaon/internal/db/schema.sql": []byte("CREATE TABLE fixture(id INTEGER PRIMARY KEY);\n"),
		"lycaon/internal/db/schema_version_lock.json": schemaLock,
		"lycaon/internal/db/released-baselines.json":  []byte(baselines),
	}
	manifest, err := json.Marshal(map[string]any{
		"app_version":     seedVersion,
		"schema_identity": map[string]any{"revision": revision, "shape": strings.Repeat("b", 64)},
	})
	contractcheck.FailErr(t, "encode seed manifest", err)
	files := map[string][]byte{
		"seed-manifest.json":                manifest,
		"scripts/upgrade-corpus-prepare.sh": []byte(contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/upgrade-corpus-prepare.sh")),
		"scripts/upgrade-corpus-seed.sh": []byte(`#!/usr/bin/env bash
set -euo pipefail
output_dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --out) output_dir="$2"; shift 2 ;;
    --sidecar) [[ "$2" == "$PW_BUILD_DIR/lycaon-dev" ]] || exit 3; shift 2 ;;
    *) exit 2 ;;
  esac
done
mkdir -p "$output_dir"
cp "$(dirname "$0")/../seed-manifest.json" "$output_dir/MANIFEST.json"
`),
	}
	for path, body := range inputs {
		files[path] = body
	}
	for path, body := range files {
		absolute := filepath.Join(root, path)
		contractcheck.FailErr(t, "create preparation fixture directory", os.MkdirAll(filepath.Dir(absolute), 0o700))
		contractcheck.FailErr(t, "write preparation fixture", os.WriteFile(absolute, body, 0o600))
	}
	return releasePreparation{root: root, paths: paths}, inputs
}
