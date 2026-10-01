package bundled_test

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func (f buildFixture) replacePayload(t *testing.T, name, key string, raw []byte) {
	t.Helper()
	writeBuildFile(t, f.directory, name, raw)
	f.provenance[key+"_sha256"] = buildDigest(raw)
	if key == "binary" || key == "source_archive" {
		f.provenance[key+"_bytes"] = len(raw)
	}
	f.writeProvenance(t)
}
func (f buildFixture) rejectAdmission(t *testing.T, reason string) {
	t.Helper()
	_, err := bundled.SelectBuildArtifact(f.source, f.directory, f.goos, f.goarch)
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("admission should reject %q, got %v", reason, err)
	}
}
func (f buildFixture) sourceManifestBytes(t *testing.T) []byte {
	t.Helper()
	file, err := os.Open(filepath.Join(f.directory, "opengrep-source.tar.gz"))
	testutil.FailErr(t, "open fixture archive", err)
	defer func() { _ = file.Close() }()
	compressed, err := gzip.NewReader(file)
	testutil.FailErr(t, "open fixture compression", err)
	defer func() { _ = compressed.Close() }()
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		testutil.FailErr(t, "find source manifest", err)
		if header.Name == "SOURCE-MANIFEST.json" {
			raw, err := io.ReadAll(io.LimitReader(reader, 16<<20))
			testutil.FailErr(t, "read source manifest", err)
			return raw
		}
	}
}

func TestAdmissionRejectsDecoyAndRewrittenSourceArchives(t *testing.T) {
	t.Run("license-only decoy", func(t *testing.T) {
		f := newBuildFixture(t)
		manifest := f.sourceManifestBytes(t)
		files := map[string][]byte{"engine/LICENSE": f.archiveFiles["engine/LICENSE"]}
		f.replacePayload(t, "opengrep-source.tar.gz", "source_archive", sourceFixtureArchive(t, files, manifest))
		f.rejectAdmission(t, "membership")
	})
	t.Run("self-consistent rewritten upstream inventory", func(t *testing.T) {
		f := newBuildFixture(t)
		f.archiveFiles["engine/upstream.c"] = []byte("different upstream source")
		f.writeArchive(t, false)
		f.rejectAdmission(t, "reviewed source inventory")
	})
	t.Run("upstream bytes differ from pinned inventory", func(t *testing.T) {
		f := newBuildFixture(t)
		manifest := f.sourceManifestBytes(t)
		f.archiveFiles["engine/upstream.c"] = []byte("different upstream source")
		f.replacePayload(t, "opengrep-source.tar.gz", "source_archive", sourceFixtureArchive(t, f.archiveFiles, manifest))
		f.rejectAdmission(t, "member differs")
	})
	t.Run("missing locked input despite refreshed inventory", func(t *testing.T) {
		f := newBuildFixture(t)
		delete(f.archiveFiles, "inputs/source/tests/tainting/example.py")
		f.writeArchive(t, true)
		f.rejectAdmission(t, "locked content")
	})
	t.Run("changed installed overlay despite refreshed inventory", func(t *testing.T) {
		f := newBuildFixture(t)
		f.archiveFiles["engine/tests/tainting/example.py"] = []byte("changed installed source")
		f.writeArchive(t, true)
		f.rejectAdmission(t, "locked content")
	})
	t.Run("duplicate manifest", func(t *testing.T) {
		f := newBuildFixture(t)
		manifest := f.sourceManifestBytes(t)
		f.archiveFiles["SOURCE-MANIFEST.json"] = manifest
		f.replacePayload(t, "opengrep-source.tar.gz", "source_archive", sourceFixtureArchive(t, f.archiveFiles, manifest))
		f.rejectAdmission(t, "duplicate")
	})
}

func contractRows(t *testing.T, f buildFixture) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.directory, "contracts.jsonl"))
	testutil.FailErr(t, "read qualification fixture", err)
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row map[string]any
		testutil.FailErr(t, "parse qualification row", json.Unmarshal([]byte(line), &row))
		result = append(result, row)
	}
	return result
}
func encodeContractRows(t *testing.T, rows []map[string]any) []byte {
	t.Helper()
	var raw []byte
	for _, row := range rows {
		raw = append(raw, buildJSON(t, row)...)
		raw = append(raw, '\n')
	}
	return raw
}

func TestAdmissionRequiresCompletePassingContractEvidence(t *testing.T) {
	cases := map[string]func([]map[string]any) []map[string]any{
		"all failed":        func(rows []map[string]any) []map[string]any { rows[0]["passed"] = false; return rows },
		"summary failure":   func(rows []map[string]any) []map[string]any { rows[1]["failed"] = 1; return rows },
		"missing case":      func(rows []map[string]any) []map[string]any { return rows[1:] },
		"missing summary":   func(rows []map[string]any) []map[string]any { return rows[:1] },
		"duplicate case":    func(rows []map[string]any) []map[string]any { return []map[string]any{rows[0], rows[0], rows[1]} },
		"unknown case":      func(rows []map[string]any) []map[string]any { rows[0]["case"] = "unreviewed-case"; return rows },
		"missing execution": func(rows []map[string]any) []map[string]any { delete(rows[0], "execution"); return rows },
		"timed out": func(rows []map[string]any) []map[string]any {
			rows[0]["execution"].(map[string]any)["timed_out"] = true
			return rows
		},
		"exit mismatch": func(rows []map[string]any) []map[string]any { rows[0]["exit_code"] = 1; return rows },
		"unsupported execution status": func(rows []map[string]any) []map[string]any {
			rows[0]["exit_code"] = 4
			rows[0]["execution"].(map[string]any)["returncode"] = 4
			return rows
		},
		"invalid evidence": func(rows []map[string]any) []map[string]any { rows[0]["evidence_valid"] = false; return rows },
		"null evidence":    func(rows []map[string]any) []map[string]any { rows[0]["evidence_valid"] = nil; return rows },
		"different result": func(rows []map[string]any) []map[string]any {
			rows[0]["expected"] = []int{1}
			rows[0]["actual"] = []int{2}
			return rows
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newBuildFixture(t)
			f.replacePayload(t, "contracts.jsonl", "contracts", encodeContractRows(t, mutate(contractRows(t, f))))
			f.rejectAdmission(t, "contract")
		})
	}
}

func platformFixture(t *testing.T, f buildFixture) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.directory, "platform-checks.json"))
	testutil.FailErr(t, "read platform fixture", err)
	var report map[string]any
	testutil.FailErr(t, "parse platform fixture", json.Unmarshal(raw, &report))
	return report
}

func TestAdmissionRequiresConsistentPlatformQualification(t *testing.T) {
	major, minor, err := platformfloor.MacOSMinParts()
	testutil.FailErr(t, "read macOS minimum", err)
	cases := map[string]func(map[string]any){
		"lower declaration":  func(r map[string]any) { r["deployment_target"] = "1.0" },
		"higher declaration": func(r map[string]any) { r["deployment_target"] = "14.0" },
		"missing image":      func(r map[string]any) { r["images"] = []any{} },
		"duplicate image":    func(r map[string]any) { r["images"] = append(r["images"].([]any), r["images"].([]any)[0]) },
		"wrong architecture": func(r map[string]any) { r["images"].([]any)[0].(map[string]any)["architectures"] = []string{"other"} },
		"higher minimum OS": func(r map[string]any) {
			r["images"].([]any)[0].(map[string]any)["deployment_versions"] = [][]int{{major + 1, 0}}
		},
		"higher minimum patch": func(r map[string]any) {
			r["images"].([]any)[0].(map[string]any)["deployment_versions"] = [][]int{{major, minor, 1}}
		},
		"negative minimum OS": func(r map[string]any) {
			r["images"].([]any)[0].(map[string]any)["deployment_versions"] = [][]int{{-1, 0}}
		},
		"unknown minimum OS": func(r map[string]any) { r["images"].([]any)[0].(map[string]any)["deployment_versions"] = [][]int{} },
		"non-system dependency": func(r map[string]any) {
			r["images"].([]any)[0].(map[string]any)["dependencies"] = []string{"/tmp/private.dylib"}
		},
		"traversing system dependency": func(r map[string]any) {
			r["images"].([]any)[0].(map[string]any)["dependencies"] = []string{"/usr/lib/../../private.dylib"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newBuildFixture(t)
			report := platformFixture(t, f)
			mutate(report)
			f.replacePayload(t, "platform-checks.json", "platform_checks", buildJSON(t, report))
			f.rejectAdmission(t, "platform")
		})
	}
	t.Run("different executable with regenerated provenance", func(t *testing.T) {
		f := newBuildFixture(t)
		f.replacePayload(t, "opengrep", "binary", []byte("another executable"))
		f.rejectAdmission(t, "different executable")
	})
	t.Run("runtime floor differs", func(t *testing.T) {
		f := newBuildFixture(t)
		f.replacePayload(t, "runtime.json", "runtime", []byte(`{"environment":{"MACOSX_DEPLOYMENT_TARGET":"14.0"}}`))
		f.rejectAdmission(t, "deployment target")
	})
	t.Run("zero-padded floor is compatible", func(t *testing.T) {
		f := newBuildFixture(t)
		report := platformFixture(t, f)
		report["images"].([]any)[0].(map[string]any)["deployment_versions"] = [][]int{{major, minor, 0}}
		f.replacePayload(t, "platform-checks.json", "platform_checks", buildJSON(t, report))
		f.selectArtifact(t)
	})
}

func TestAdmissionAcceptsCompatibleDeploymentTargets(t *testing.T) {
	major, minor, err := platformfloor.MacOSMinParts()
	testutil.FailErr(t, "read macOS minimum", err)
	for _, target := range []string{fmt.Sprintf("%d.0", major-1), platformfloor.MacOSMin(), fmt.Sprintf("%d.%d.0", major, minor)} {
		t.Run(target, func(t *testing.T) {
			f := newBuildFixtureWithDeploymentTarget(t, target)
			f.selectArtifact(t)
		})
	}
}

func TestAdmissionRejectsInvalidOrUnsupportedDeploymentTargets(t *testing.T) {
	major, minor, err := platformfloor.MacOSMinParts()
	testutil.FailErr(t, "read macOS minimum", err)
	for _, target := range []string{
		"", "invalid", "13", "13.x", "13..0", "13.0.0.0", "-1.0", "+13.0",
		fmt.Sprintf("%d.0", major+1), fmt.Sprintf("%d.%d", major, minor+1), fmt.Sprintf("%d.%d.1", major, minor),
	} {
		t.Run(target, func(t *testing.T) {
			f := newBuildFixtureWithDeploymentTarget(t, target)
			f.rejectAdmission(t, "platform deployment target")
		})
	}
}
