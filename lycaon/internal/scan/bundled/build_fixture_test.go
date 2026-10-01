package bundled_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

type buildFixture struct {
	directory, goos, goarch string
	source                  *bundled.Manifest
	provenance              map[string]any
	archiveFiles            map[string][]byte
}

func sourceManifest() *bundled.Manifest {
	return &bundled.Manifest{OpenGrep: bundled.OpenGrepManifest{Version: "1.29.0+paintedwolf.26", Origin: "downstream", UpstreamVersion: "1.29.0", BaseRevision: strings.Repeat("a", 40), Revision: 26, SourceLockSHA256: strings.Repeat("b", 64), SourceManifestSHA256: strings.Repeat("c", 64), License: "LGPL-2.1", Artifacts: []bundled.ReleaseArtifact{{GOOS: "darwin", GOARCH: "arm64", URL: "https://example.invalid/opengrep.tar.gz", SHA256: strings.Repeat("d", 64), Bytes: 1}}}}
}
func buildDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func buildJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	testutil.FailErr(t, "encode fixture", err)
	return raw
}
func writeBuildFile(t *testing.T, directory, name string, raw []byte) {
	t.Helper()
	testutil.FailErr(t, "write build fixture", os.WriteFile(filepath.Join(directory, name), raw, 0o755))
}

func newBuildFixture(t *testing.T) buildFixture {
	t.Helper()
	return newBuildFixtureWithDeploymentTarget(t, "13.0")
}

func newBuildFixtureWithDeploymentTarget(t *testing.T, target string) buildFixture {
	t.Helper()
	f := buildFixture{directory: t.TempDir(), source: sourceManifest(), goos: "darwin", goarch: "arm64"}
	if runtime.GOOS == "darwin" {
		f.goarch = runtime.GOARCH
	}
	inputs := map[string][]byte{
		"source/tests/tainting/example.yaml": []byte("rules: []\n"), "source/tests/tainting/example.py": []byte("sink(source())\n"),
		"source/tests/rule-translation.json": []byte("[]"), "source/tests/native-file-selection.json": []byte("[]"),
		"source/tests/callback-rule-validation.json": []byte("[]"), "source/tests/model-rule-validation.json": []byte("[]"), "source/tests/native-pattern-validation.json": []byte("[]"),
		"locks/runtimes.json": buildJSON(t, map[string]any{"macos": map[string]string{"deployment_target": target}}),
	}
	locked := make(map[string]string)
	f.archiveFiles = map[string][]byte{"engine/LICENSE": []byte("maintained source license\n"), "engine/upstream.c": []byte("int upstream(void) { return 1; }\n")}
	for name, raw := range inputs {
		locked[name] = buildDigest(raw)
		f.archiveFiles["inputs/"+name] = raw
		if strings.HasPrefix(name, "source/") {
			f.archiveFiles["engine/"+strings.TrimPrefix(name, "source/")] = raw
		}
	}
	lock := buildJSON(t, map[string]any{"revision": f.source.OpenGrep.BaseRevision, "upstream_version": "1.29.0", "patch_version": 26, "interfaces_revision": strings.Repeat("d", 40), "files": locked, "grammars": []any{}})
	f.source.OpenGrep.SourceLockSHA256 = buildDigest(lock)
	f.archiveFiles["inputs/source-lock.json"] = lock
	executable := []byte("finalized signed executable bytes")
	architecture := f.goarch
	if architecture == "amd64" {
		architecture = "x86_64"
	}
	f.provenance = map[string]any{"schema_version": 1, "version": f.source.OpenGrep.Version, "upstream_revision": f.source.OpenGrep.BaseRevision, "source_lock_sha256": buildDigest(lock), "platform": f.goos, "architecture": architecture, "binary_sha256": buildDigest(executable), "binary_bytes": len(executable)}
	contracts := []byte("{\"case\":\"source/tests/tainting/example.py\",\"passed\":true,\"exit_code\":0,\"execution\":{\"returncode\":0,\"timed_out\":false}}\n{\"contracts\":1,\"failed\":0}\n")
	platform := buildJSON(t, map[string]any{"deployment_target": target, "images": []any{map[string]any{"path": "opengrep", "sha256": buildDigest(executable), "architectures": []string{architecture}, "deployment_versions": [][]int{{11, 0}}}}})
	files := map[string][]byte{"NOTICES-opengrep.md": []byte("Qualified engine notices\n"), "source-lock.json": lock, "LICENSE": f.archiveFiles["engine/LICENSE"], "opengrep": executable, "contracts.jsonl": contracts, "platform-checks.json": platform, "runtime.json": buildJSON(t, map[string]any{"environment": map[string]string{"MACOSX_DEPLOYMENT_TARGET": target}})}
	for name, field := range map[string]string{"contracts.jsonl": "contracts_sha256", "platform-checks.json": "platform_checks_sha256", "runtime.json": "runtime_sha256"} {
		f.provenance[field] = buildDigest(files[name])
	}
	for name, raw := range files {
		writeBuildFile(t, f.directory, name, raw)
	}
	f.writeArchive(t, true)
	return f
}

func (f buildFixture) writeArchive(t *testing.T, reviewManifest bool) {
	t.Helper()
	records := make(map[string]any)
	for name, raw := range f.archiveFiles {
		records[name] = map[string]any{"sha256": buildDigest(raw), "bytes": len(raw)}
	}
	manifest := buildJSON(t, map[string]any{"identity": map[string]string{"base_revision": f.source.OpenGrep.BaseRevision, "interfaces_revision": strings.Repeat("d", 40), "source_lock_sha256": f.source.OpenGrep.SourceLockSHA256}, "files": records})
	if reviewManifest {
		f.source.OpenGrep.SourceManifestSHA256 = buildDigest(manifest)
	}
	raw := sourceFixtureArchive(t, f.archiveFiles, manifest)
	writeBuildFile(t, f.directory, "opengrep-source.tar.gz", raw)
	f.provenance["source_archive_sha256"] = buildDigest(raw)
	f.provenance["source_archive_bytes"] = len(raw)
	f.writeProvenance(t)
}

func sourceFixtureArchive(t *testing.T, files map[string][]byte, manifest []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(compressed)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := files[name]
		testutil.FailErr(t, "source member header", writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(raw)), Typeflag: tar.TypeReg}))
		_, err := writer.Write(raw)
		testutil.FailErr(t, "source member bytes", err)
	}
	testutil.FailErr(t, "source manifest header", writer.WriteHeader(&tar.Header{Name: "SOURCE-MANIFEST.json", Mode: 0o644, Size: int64(len(manifest)), Typeflag: tar.TypeReg}))
	_, err := writer.Write(manifest)
	testutil.FailErr(t, "source manifest bytes", err)
	testutil.FailErr(t, "close source tar", writer.Close())
	testutil.FailErr(t, "close source compression", compressed.Close())
	return buffer.Bytes()
}

func (f buildFixture) writeProvenance(t *testing.T) {
	t.Helper()
	writeBuildFile(t, f.directory, "provenance.json", buildJSON(t, f.provenance))
}
func (f buildFixture) selectArtifact(t *testing.T) *bundled.Manifest {
	t.Helper()
	m, err := bundled.SelectBuildArtifact(f.source, f.directory, f.goos, f.goarch)
	testutil.FailErr(t, "select build artifact", err)
	return m
}
func (f buildFixture) runtimeManifest(t *testing.T) *bundled.Manifest {
	t.Helper()
	encoded, err := bundled.EncodedBuildIdentity(f.selectArtifact(t))
	testutil.FailErr(t, "encode build identity", err)
	m, err := bundled.ManifestWithBuildIdentity(f.source, encoded)
	testutil.FailErr(t, "apply runtime identity", err)
	return m
}

func (f buildFixture) runtimeManifestForHost(t *testing.T) *bundled.Manifest {
	t.Helper()
	encoded, err := bundled.EncodedBuildIdentity(f.selectArtifact(t))
	testutil.FailErr(t, "encode build identity", err)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	testutil.FailErr(t, "decode runtime fixture identity", err)
	var identity bundled.BuildIdentity
	testutil.FailErr(t, "read runtime fixture identity", json.Unmarshal(raw, &identity))
	identity.GOOS, identity.GOARCH = runtime.GOOS, runtime.GOARCH
	m, err := bundled.ManifestWithBuildIdentity(f.source, base64.StdEncoding.EncodeToString(buildJSON(t, identity)))
	testutil.FailErr(t, "apply runtime identity", err)
	return m
}
