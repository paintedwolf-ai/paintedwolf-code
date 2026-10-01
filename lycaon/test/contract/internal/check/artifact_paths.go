package check

import (
	"os"
	"path/filepath"
	"testing"
)

// ArtifactPaths names the resolver-owned directories of a fixture checkout.
type ArtifactPaths struct {
	Artifacts, Bin, Build, Locks string
}

// InstallArtifactPaths copies the artifact path resolver into the fixture
// checkout at root and places every directory it resolves under dir.
func InstallArtifactPaths(t *testing.T, root, dir string) ArtifactPaths {
	t.Helper()
	repo := RepoRoot(t)
	for _, name := range []string{"artifact-paths.sh", "artifact_paths.py"} {
		destination := filepath.Join(root, "scripts", name)
		FailErr(t, "create resolver directory", os.MkdirAll(filepath.Dir(destination), 0o700))
		FailErr(t, "copy "+name, os.WriteFile(destination, []byte(ReadRepoFile(t, repo, "scripts/"+name)), 0o700))
	}
	return ArtifactPaths{
		Artifacts: filepath.Join(dir, "artifacts"),
		Bin:       filepath.Join(dir, "tools"),
		Build:     filepath.Join(dir, "build"),
		Locks:     filepath.Join(dir, "locks"),
	}
}

// Env overrides the resolver variables a verification run exports, so it goes
// after any inherited environment.
func (p ArtifactPaths) Env() []string {
	return []string{
		"PW_ARTIFACT_ROOT=" + p.Artifacts,
		"PW_TEST_ARTIFACT_ROOT=" + p.Artifacts,
		"PW_BIN_DIR=" + p.Bin,
		"PW_BUILD_DIR=" + p.Build,
		"PW_LOCK_ROOT=" + p.Locks,
	}
}
