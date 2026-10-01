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
	for _, fixture := range maintainedScanFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			project := t.TempDir()
			for name, source := range fixture.sources {
				testutil.FailErr(t, "write maintained scan fixture", os.WriteFile(
					filepath.Join(project, name), []byte(source), 0o600))
			}
			created := enqueueCodeScan(t, h.Server, project, []wire.ScanCategory{wire.ScanCategorySAST})
			got := waitScanComplete(t, h.Server, created.ProjectID, created.ID, bundledScannerWaitBudget)
			assertMaintainedScanResult(t, fixture, got)
			assertMaintainedExecutionIdentity(t, manifest, binary, got)
		})
	}
	t.Run("ordinary_project_diagnostics", func(t *testing.T) {
		assertOrdinaryProjectScan(t, h, manifest, binary)
	})
}

type maintainedScanFixture struct {
	name     string
	sources  map[string]string
	rule     string
	coverage wire.ScanCoverageStatus
	warning  wire.ScanWarningKind
}

func maintainedScanFixtures() []maintainedScanFixture {
	const unsafeSwift = "import Foundation\nfunc load(decoder: NSKeyedUnarchiver) { decoder.requiresSecureCoding = false }\n"
	return []maintainedScanFixture{
		{
			name: "finding", sources: map[string]string{"app.swift": unsafeSwift},
			rule: "opengrep:lycaon.swift.secure-coding-disabled", coverage: wire.ScanCoverageComplete,
		},
		{
			name: "quiet", coverage: wire.ScanCoverageComplete,
			sources: map[string]string{
				"safe.swift":    "import Foundation\nfunc load(decoder: NSKeyedUnarchiver) { decoder.requiresSecureCoding = true }\n",
				"comment.swift": "/*\n" + unsafeSwift + "*/\n",
			},
		},
		{
			name: "partial_without_findings", coverage: wire.ScanCoveragePartial,
			warning: wire.ScanWarningFilePartialSemantics,
			sources: map[string]string{"app.dart": `import 'dart:io' as io;
import 'package:shelf/shelf.dart' as shelf;
Future<shelf.Response> handler(shelf.Request request) async {
 final other = request.context['other'] as dynamic;
 final cmd = other.url.queryParameters['cmd'];
 await io.Process.run('sh', ['-c', cmd]);
 return shelf.Response.ok('ok');
}
`},
		},
	}
}

func assertMaintainedScanResult(t *testing.T, fixture maintainedScanFixture, got wire.CodeScan) {
	t.Helper()
	if got.Status != wire.CodeScanStatusComplete || got.CoverageStatus != fixture.coverage {
		t.Fatalf("scan status=%s coverage=%s error=%s; want complete/%s",
			got.Status, got.CoverageStatus, got.Error, fixture.coverage)
	}
	if fixture.rule == "" {
		if got.FindingsCount != 0 || len(got.Findings) != 0 {
			t.Fatalf("unexpected findings: count=%d findings=%+v", got.FindingsCount, got.Findings)
		}
	} else if got.FindingsCount != 1 || len(got.Findings) != 1 || got.Findings[0].RuleID != fixture.rule {
		t.Fatalf("want one %s finding; count=%d findings=%+v", fixture.rule, got.FindingsCount, got.Findings)
	}
	if fixture.warning == "" {
		if len(got.Warnings) != 0 {
			t.Fatalf("unexpected warnings: %+v", got.Warnings)
		}
		return
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Kind != fixture.warning ||
		got.Warnings[0].Construct != "model_type_unresolved" || filepath.Base(got.Warnings[0].File) != "app.dart" {
		t.Fatalf("partial semantic warning was not preserved: %+v", got.Warnings)
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
