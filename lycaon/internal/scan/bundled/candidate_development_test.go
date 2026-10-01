//go:build !paintedwolf_release

package bundled_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

type candidateFixture struct {
	directory  string
	binary     string
	payload    []byte
	provenance map[string]any
	lock       map[string]any
}

func newCandidateFixture(t *testing.T) candidateFixture {
	t.Helper()
	root := t.TempDir()
	architecture := runtime.GOARCH
	if architecture == "amd64" {
		architecture = "x86_64"
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		architecture = "aarch64"
	}
	f := candidateFixture{directory: root, binary: bundled.BinaryPath(root), payload: []byte("candidate executable bytes"),
		lock: map[string]any{"revision": strings.Repeat("a", 40), "upstream_version": "1.29.0", "patch_version": 26},
		provenance: map[string]any{"schema_version": 1, "upstream_revision": strings.Repeat("a", 40),
			"platform": runtime.GOOS, "architecture": architecture, "version": "1.29.0+paintedwolf.26"},
	}
	testutil.FailErr(t, "write candidate executable", os.WriteFile(f.binary, f.payload, 0o755))
	f.provenance["binary_sha256"] = fixtureDigest(f.payload)
	f.provenance["binary_bytes"] = len(f.payload)
	lock := fixtureJSON(t, f.lock)
	f.provenance["source_lock_sha256"] = fixtureDigest(lock)
	testutil.FailErr(t, "write source lock", os.WriteFile(filepath.Join(root, "source-lock.json"), lock, 0o600))
	f.writeProvenance(t)
	return f
}

func fixtureDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func fixtureJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	testutil.FailErr(t, "encode candidate metadata", err)
	return raw
}

func (f candidateFixture) writeProvenance(t *testing.T) {
	t.Helper()
	testutil.FailErr(t, "write candidate provenance", os.WriteFile(filepath.Join(f.directory, "provenance.json"), fixtureJSON(t, f.provenance), 0o600))
}

func TestLocalCandidateIdentityAndExactResolution(t *testing.T) {
	f := newCandidateFixture(t)
	f.provenance["actual_minimum_os_execution"] = false
	f.provenance["other_platforms"] = []string{"not qualified"}
	f.writeProvenance(t)
	t.Setenv(bundled.EnvOpenGrepCandidate, f.directory)
	m, err := bundled.LoadManifest()
	testutil.FailErr(t, "select local candidate", err)
	if m.OpenGrep.Version != f.provenance["version"] || m.OpenGrep.BaseRevision != f.lock["revision"] ||
		m.OpenGrep.SourceLockSHA256 != f.provenance["source_lock_sha256"] || m.OpenGrep.Origin != "downstream" {
		t.Fatalf("candidate identity lost: %+v", m.OpenGrep)
	}
	artifact, err := m.ArtifactForCurrentPlatform()
	testutil.FailErr(t, "select candidate platform", err)
	if artifact.SHA256 != f.provenance["binary_sha256"] {
		t.Fatalf("candidate platform identity differs: %+v", artifact)
	}
	path, err := bundled.ResolveOpenGrepBinary(m, t.TempDir(), t.TempDir())
	testutil.FailErr(t, "resolve exact candidate", err)
	if path != f.binary {
		t.Fatalf("candidate path = %q, want %q", path, f.binary)
	}
	if strings.Contains(string(fixtureJSON(t, m)), f.directory) {
		t.Fatal("local candidate path escaped serialized manifest")
	}
}

func TestLocalCandidateRejectsInvalidIdentity(t *testing.T) {
	cases := map[string]func(candidateFixture){
		"schema":                   func(f candidateFixture) { f.provenance["schema_version"] = 2 },
		"platform":                 func(f candidateFixture) { f.provenance["platform"] = "another-os" },
		"architecture":             func(f candidateFixture) { f.provenance["architecture"] = "another-arch" },
		"binary digest":            func(f candidateFixture) { f.provenance["binary_sha256"] = strings.Repeat("b", 64) },
		"binary size":              func(f candidateFixture) { f.provenance["binary_bytes"] = len(f.payload) + 1 },
		"empty executable":         func(f candidateFixture) { f.provenance["binary_bytes"] = 0 },
		"version":                  func(f candidateFixture) { f.provenance["version"] = "1.29.0+paintedwolf.27" },
		"base revision":            func(f candidateFixture) { f.provenance["upstream_revision"] = strings.Repeat("b", 40) },
		"source lock":              func(f candidateFixture) { f.provenance["source_lock_sha256"] = strings.Repeat("b", 64) },
		"invalid owned field type": func(f candidateFixture) { f.provenance["binary_bytes"] = "twenty" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCandidateFixture(t)
			mutate(f)
			f.writeProvenance(t)
			t.Setenv(bundled.EnvOpenGrepCandidate, f.directory)
			if _, err := bundled.LoadManifest(); err == nil {
				t.Fatal("invalid candidate accepted")
			}
		})
	}
}

func TestLocalCandidateRejectsMissingOrRelativeSelection(t *testing.T) {
	for _, directory := range []string{"relative/artifact", filepath.Join(t.TempDir(), "absent"), " "} {
		t.Run(directory, func(t *testing.T) {
			t.Setenv(bundled.EnvOpenGrepCandidate, directory)
			if _, err := bundled.LoadManifest(); err == nil {
				t.Fatal("invalid explicit selection fell back to shipping manifest")
			}
		})
	}
}

func TestLocalCandidateProvenanceRequiresOneObject(t *testing.T) {
	for _, suffix := range []string{"\n{}", "\ntrue", "trailing bytes"} {
		t.Run(suffix, func(t *testing.T) {
			f := newCandidateFixture(t)
			raw := append(fixtureJSON(t, f.provenance), []byte(suffix)...)
			testutil.FailErr(t, "append trailing provenance", os.WriteFile(filepath.Join(f.directory, "provenance.json"), raw, 0o600))
			t.Setenv(bundled.EnvOpenGrepCandidate, f.directory)
			if _, err := bundled.LoadManifest(); err == nil {
				t.Fatal("candidate with trailing provenance accepted")
			}
		})
	}
}
