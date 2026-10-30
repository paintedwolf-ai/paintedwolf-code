//go:build linux

package launchguard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func artifactEvidenceFixture(t *testing.T) (desktopArtifact, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "payload")
	testutil.FailErr(t, "artifact payload root", os.Mkdir(root, 0o755))
	var payload []pkgmanifest.PayloadEntry
	for _, name := range []string{"bin", "engine-root", "engine-root/schemas", "engine-root/schemas/oar"} {
		testutil.FailErr(t, "fixture directory", os.Mkdir(filepath.Join(root, name), 0o755))
		payload = append(payload, pkgmanifest.PayloadEntry{Path: name, Kind: "directory", Mode: 0o755})
	}
	var executables []pkgmanifest.Executable
	for _, name := range []string{"bin/painted-wolf-code", "bin/pw", "bin/pw-logs", "bin/pw-vault-identity", "engine-root/schemas/oar/oar.schema.json"} {
		raw := []byte("artifact inventory fixture " + name)
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		size := int64(len(raw))
		testutil.FailErr(t, "fixture file", os.WriteFile(filepath.Join(root, name), raw, 0o555))
		payload = append(payload, pkgmanifest.PayloadEntry{Path: name, Kind: "file", Mode: 0o555, SHA256: &digest, Size: &size})
		executables = append(executables, pkgmanifest.Executable{Path: filepath.Join("/opt/painted-wolf-code", name), SHA256: digest, Size: size})
	}
	write := func(name string, value any) (string, string) {
		raw, err := json.Marshal(value)
		testutil.FailErr(t, "fixture JSON", err)
		path := filepath.Join(dir, name)
		if name == "manifest.json" {
			path = filepath.Join(root, name)
		}
		testutil.FailErr(t, "fixture evidence", os.WriteFile(path, raw, 0o644))
		sum := sha256.Sum256(raw)
		return path, hex.EncodeToString(sum[:])
	}
	_, manifestHash := write("manifest.json", pkgmanifest.Manifest{FormatVersion: pkgmanifest.FormatVersion, Package: "painted-wolf-code", Version: "1.0.3", Architecture: fixtureArchitecture(), InstallRoot: "/opt/painted-wolf-code", Executables: executables, Payload: payload})
	job, jobHash := write("job.json", map[string]any{"job_id": "own-build", "arguments": []string{"den:bundle"}})
	result, resultHash := write("result.json", map[string]any{"job_id": "own-build", "state": "completed", "exit_code": 0})
	tree := strings.Repeat("a", 40)
	return desktopArtifact{LibraryRoots: []string{"/usr/lib"}, Version: 1, Root: root, SourceCommit: strings.Repeat("b", 40), SourceTree: tree, BeforeTree: tree, AfterTree: tree, ManifestSHA256: manifestHash, PreparationJob: job, PreparationJobSHA256: jobHash, PreparationResult: result, PreparationResultSHA256: resultHash}, filepath.Join(dir, "artifact.json")
}

func TestDesktopArtifactRequiresCompleteSameSourceSuccessfulPreparation(t *testing.T) {
	artifact, path := artifactEvidenceFixture(t)
	write := func(value desktopArtifact) {
		raw, err := json.Marshal(value)
		testutil.FailErr(t, "artifact encoding", err)
		testutil.FailErr(t, "artifact input", os.WriteFile(path, raw, 0o644))
	}
	write(artifact)
	if _, _, err := loadDesktopArtifact(path, artifact.SourceTree); err != nil {
		t.Fatalf("valid inventory/provenance fixture refused: %v", err)
	}
	for _, mutate := range []func(*desktopArtifact){
		func(a *desktopArtifact) { a.LibraryRoots = nil },
		func(a *desktopArtifact) { a.LibraryRoots = []string{"/"} },
		func(a *desktopArtifact) { a.LibraryRoots = []string{"relative"} },
		func(a *desktopArtifact) { a.LibraryRoots = []string{"/usr/lib/../lib"} },
		func(a *desktopArtifact) { a.LibraryRoots = []string{"/usr/lib", "/usr/lib"} },
		func(a *desktopArtifact) { a.LibraryRoots = make([]string, 33) },
		func(a *desktopArtifact) { a.Version = 2 }, func(a *desktopArtifact) { a.SourceTree = strings.Repeat("c", 40) },
		func(a *desktopArtifact) { a.BeforeTree = strings.Repeat("c", 40) }, func(a *desktopArtifact) { a.AfterTree = strings.Repeat("c", 40) },
		func(a *desktopArtifact) { a.PreparationResultSHA256 = strings.Repeat("0", 64) }, func(a *desktopArtifact) { a.PreparationJobSHA256 = strings.Repeat("0", 64) },
		func(a *desktopArtifact) { a.ManifestSHA256 = strings.Repeat("0", 64) }, func(a *desktopArtifact) { a.Root = "relative" },
	} {
		invalid := artifact
		mutate(&invalid)
		write(invalid)
		if _, _, err := loadDesktopArtifact(path, artifact.SourceTree); err == nil {
			t.Fatal("invalid artifact evidence accepted")
		}
	}
	write(artifact)
	testutil.FailErr(t, "tamper staged payload", os.Chmod(filepath.Join(artifact.Root, "bin/pw"), 0o755))
	if _, _, err := loadDesktopArtifact(path, artifact.SourceTree); err == nil {
		t.Fatal("changed payload accepted despite manifest hashes")
	}
	if _, _, err := loadDesktopArtifact(filepath.Join(t.TempDir(), "missing"), artifact.SourceTree); err == nil {
		t.Fatal("missing explicit artifact accepted")
	}
}

func TestRealDesktopSelectionRefusesMissingArtifactWithoutRunningTheGoLeg(t *testing.T) {
	command := exec.Command("bash", filepath.Join("..", "..", "..", "scripts", "test-confine-linux.sh"), "launch-desktop")
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PW_LAUNCH_DESKTOP_ARTIFACT=") {
			command.Env = append(command.Env, value)
		}
	}
	output, err := command.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 2 || !strings.Contains(string(output), "unverified: launch-desktop requires") {
		t.Fatalf("missing real-desktop input did not refuse qualification: %v\n%s", err, output)
	}
}
