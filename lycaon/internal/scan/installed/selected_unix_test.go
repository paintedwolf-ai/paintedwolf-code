//go:build darwin || linux

package installed

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func fixtureDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func selectedFixture(t *testing.T) (*bundled.Manifest, string) {
	t.Helper()
	selected, err := bundled.LoadManifest()
	testutil.FailErr(t, "load source selection for fixture", err)
	source := &bundled.Manifest{OpenGrep: selected.OpenGrep}
	root := t.TempDir()
	directory := bundled.EngineBundledDir(root, source.OpenGrep.Version)
	testutil.FailErr(t, "fixture artifact directory", os.MkdirAll(directory, 0o700))
	const executable = `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = --output ]; then
    shift
    printf '%s' '{"results":[],"errors":[],"paths":{"scanned":[]}}' > "$1"
    exit 0
  fi
  shift
done
exit 2
`
	identity := bundled.BuildIdentity{SchemaVersion: 1, Version: source.OpenGrep.Version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, BinarySHA256: fixtureDigest([]byte(executable)), BinaryBytes: int64(len(executable))}
	for _, name := range []string{"opengrep-source.tar.gz", "source-lock.json", "provenance.json", "LICENSE", "contracts.jsonl", "platform-checks.json", "runtime.json", "NOTICES-opengrep.md"} {
		raw := []byte("fixture " + name)
		digest := fixtureDigest(raw)
		identity.Payload = append(identity.Payload, bundled.PayloadIdentity{Name: name, SHA256: digest, Bytes: int64(len(raw))})
		if name == "source-lock.json" {
			source.OpenGrep.SourceLockSHA256 = digest
		}
		testutil.FailErr(t, "write fixture payload", os.WriteFile(filepath.Join(directory, name), raw, 0o600))
	}
	identity.SourceLockSHA256 = source.OpenGrep.SourceLockSHA256
	identity.SourceManifestSHA256 = source.OpenGrep.SourceManifestSHA256
	raw, err := json.Marshal(identity)
	testutil.FailErr(t, "encode fixture authority", err)
	manifest, err := bundled.ManifestWithBuildIdentity(source, base64.StdEncoding.EncodeToString(raw))
	testutil.FailErr(t, "bind fixture authority", err)
	testutil.FailErr(t, "write fixture executable", os.WriteFile(filepath.Join(directory, "opengrep"), []byte(executable), 0o700))
	return manifest, root
}

func TestSelectedScannerCannotQualifyAnEmptySuccessfulReport(t *testing.T) {
	manifest, root := selectedFixture(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	report, err := VerifyArtifact(t.Context(), root, manifest)
	if err == nil || report.Status != "failed" || report.Code != "fixture_mismatch" || !report.Confined || len(report.Cases) != len(fixtures) {
		t.Fatalf("empty engine report=%+v error=%v", report, err)
	}
	t.Run("explicit off is unverified", func(t *testing.T) {
		t.Setenv("LYCAON_SANDBOX", "off")
		report, err := VerifyArtifact(t.Context(), root, manifest)
		if err == nil || report.Status != "unverified" || report.Code != "confinement_unavailable" || report.Confined {
			t.Fatalf("disabled qualification report=%+v error=%v", report, err)
		}
	})
	t.Run("changed installed executable is refused", func(t *testing.T) {
		binary := bundled.ArtifactPath(root, manifest.OpenGrep.Version, runtime.GOOS)
		testutil.FailErr(t, "change fixture bytes", os.WriteFile(binary, []byte("changed"), 0o700))
		report, err := VerifyArtifact(t.Context(), root, manifest)
		if err == nil || report.Code != "artifact_refused" || report.Confined {
			t.Fatalf("changed artifact report=%+v error=%v", report, err)
		}
	})
}
