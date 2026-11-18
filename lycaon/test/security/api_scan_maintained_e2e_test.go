//go:build integration

package security

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestAPIScanMaintainedSASTE2E(t *testing.T) {
	testutil.SkipIfShort(t, "runs the default maintained scanner")
	t.Setenv(bundled.EnvOpenGrepCandidate, "")
	manifest, err := bundled.LoadManifest()
	testutil.FailErr(t, "load maintained manifest", err)
	if manifest.OpenGrep.Origin != "downstream" {
		t.Fatalf("selected maintained origin = %q", manifest.OpenGrep.Origin)
	}
	binary, err := bundled.ResolveOpenGrepBinary(manifest, t.TempDir(), configlayout.EngineRoot())
	if err != nil {
		testutil.MissingScannerResource(t, "selected maintained engine", err)
	}
	testutil.FailErr(t, "verify selected source lock", bundled.VerifyFileSHA256(
		filepath.Join(filepath.Dir(binary), "source-lock.json"), manifest.OpenGrep.SourceLockSHA256))
	h := wiring.BuildForTest(t, wiring.WithBundledScanners())
	t.Cleanup(h.StartBackgroundWorkers(t, t.Context()))
	t.Run("finding_quiet_and_partial_files", func(t *testing.T) {
		project := t.TempDir()
		for name, source := range maintainedScanSources() {
			testutil.FailErr(t, "write maintained scan fixture", os.WriteFile(filepath.Join(project, name), []byte(source), 0o600))
		}
		created := enqueueCodeScan(t, h.Server, project, []wire.ScanCategory{wire.ScanCategorySAST})
		got := waitScanComplete(t, h.Server, created.ProjectID, created.ID)
		assertMaintainedScanResult(t, got)
		assertMaintainedExecutionIdentity(t, manifest, binary, got)
	})
	t.Run("ordinary_project_diagnostics", func(t *testing.T) {
		assertOrdinaryProjectScan(t, h, manifest, binary)
	})
}

// maintainedScanSources is one project the maintained engine scans once:
// app.swift holds the one finding, safe.swift and comment.swift must stay
// quiet, and app.dart has partial semantics but no finding.
func maintainedScanSources() map[string]string {
	const unsafeSwift = "import Foundation\nfunc load(decoder: NSKeyedUnarchiver) { decoder.requiresSecureCoding = false }\n"
	return map[string]string{
		"app.swift":     unsafeSwift,
		"safe.swift":    "import Foundation\nfunc load(decoder: NSKeyedUnarchiver) { decoder.requiresSecureCoding = true }\n",
		"comment.swift": "/*\n" + unsafeSwift + "*/\n",
		"app.dart": `import 'dart:io' as io;
import 'package:shelf/shelf.dart' as shelf;
Future<shelf.Response> handler(shelf.Request request) async {
 final other = request.context['other'] as dynamic;
 final cmd = other.url.queryParameters['cmd'];
 await io.Process.run('sh', ['-c', cmd]);
 return shelf.Response.ok('ok');
}
`,
	}
}

func assertMaintainedScanResult(t *testing.T, got wire.CodeScan) {
	t.Helper()
	if got.Status != wire.CodeScanStatusComplete || got.CoverageStatus != wire.ScanCoveragePartial {
		t.Fatalf("scan status=%s coverage=%s error=%s; want complete/partial", got.Status, got.CoverageStatus, got.Error)
	}
	const rule = "opengrep:lycaon.swift.secure-coding-disabled"
	if got.FindingsCount != 1 || len(got.Findings) != 1 || got.Findings[0].RuleID != rule ||
		len(got.Findings[0].Locations) == 0 || filepath.Base(got.Findings[0].Locations[0].URI) != "app.swift" {
		t.Fatalf("want one %s finding in app.swift; count=%d findings=%+v", rule, got.FindingsCount, got.Findings)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Kind != wire.ScanWarningFilePartialSemantics ||
		got.Warnings[0].Construct != "model_type_unresolved" || filepath.Base(got.Warnings[0].File) != "app.dart" {
		t.Fatalf("want one partial semantics warning for app.dart: %+v", got.Warnings)
	}
}

func assertMaintainedExecutionIdentity(t *testing.T, selected *bundled.Manifest, binary string, got wire.CodeScan) {
	t.Helper()
	manifest := got.ExecutionManifest
	if manifest == nil || got.ExecutionFingerprint == "" {
		t.Fatal("persisted scan omitted its execution identity")
	}
	artifact, err := selected.ArtifactForCurrentPlatform()
	testutil.FailErr(t, "selected maintained engine platform", err)
	if manifest.EngineVersion != selected.OpenGrep.Version || manifest.EngineSHA256 != artifact.SHA256 {
		t.Fatalf("persisted engine identity = %s/%s; selected %s/%s", manifest.EngineVersion,
			manifest.EngineSHA256, selected.OpenGrep.Version, artifact.SHA256)
	}
	testutil.FailErr(t, "verify executed maintained engine bytes", bundled.VerifyFileSHA256(binary, manifest.EngineSHA256))
	if manifest.RulesSHA256 == "" || manifest.ExclusionsSHA256 == "" || got.SourceSnapshotID == "" {
		t.Fatalf("scan omitted rule or project source identity: manifest=%+v snapshot=%q", manifest, got.SourceSnapshotID)
	}
}
