package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

func writeReleaseTree(t *testing.T, repo string, files treeRelease) {
	t.Helper()
	for rel, data := range files {
		target := filepath.Join(repo, filepath.FromSlash(rel))
		testutil.FailErr(t, "create release tree", os.MkdirAll(filepath.Dir(target), 0o750))
		testutil.FailErr(t, "write release tree", os.WriteFile(target, data, 0o600))
	}
}

func TestReleasedWorkflowRequiresPreservedBytesAndAdvancingVersions(t *testing.T) {
	for _, tc := range []struct {
		name, change, want string
		archive, remove    bool
	}{
		{name: "unchanged"},
		{name: "copy without version", change: "copy", want: "without a version bump"},
		{name: "new version without archive", change: "2.0.0", want: "archive"},
		{name: "archived version", change: "2.0.0", archive: true},
		{name: "version downgrade", change: "0.9.0", archive: true, want: "advance beyond"},
		{name: "removed with archive", archive: true, remove: true},
		{name: "removed without archive", remove: true, want: "archive"},
		{name: "altered archive", change: "bad archive", archive: true, remove: true, want: "differs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := committedRelease(t)
			repo := t.TempDir()
			writeReleaseTree(t, repo, release)
			live := filepath.Join(repo, filepath.FromSlash(securityPack), "workflows", "security-survey", "workflow.yaml")
			if tc.archive {
				_, err := seal(release, securityPack, "security-survey", filepath.Join(repo, filepath.FromSlash(securityPack)), false)
				testutil.FailErr(t, "seal baseline", err)
			}
			switch tc.change {
			case "copy":
				file := filepath.Join(repo, filepath.FromSlash(securityPack), "guidance", "coordinator-security-plan.md")
				testutil.FailErr(t, "change live guidance", os.WriteFile(file, []byte("changed\n"), 0o600))
			case "2.0.0", "0.9.0":
				raw, err := os.ReadFile(live)
				testutil.FailErr(t, "read live manifest", err)
				testutil.FailErr(t, "advance live version", os.WriteFile(live, bytes.ReplaceAll(raw, []byte("version: 1.0.0"), []byte("version: "+tc.change)), 0o600))
			case "bad archive":
				file := filepath.Join(repo, filepath.FromSlash(securityPack), "archive", "security-survey", "1.0.0", "guidance", "coordinator-security-plan.md")
				testutil.FailErr(t, "alter archive", os.WriteFile(file, []byte("changed\n"), 0o600))
			}
			if tc.remove {
				testutil.FailErr(t, "remove live workflow", os.Remove(live))
			}
			err := checkReleasedWorkflow(release, repo, securityPack, "security-survey")
			if tc.want == "" {
				testutil.FailErr(t, "check preserved workflow", err)
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("check=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestCheckAllDiscoversStableWorkflowBaselines(t *testing.T) {
	repo := t.TempDir()
	files := committedRelease(t)
	files["VERSION"] = []byte("1.0.1\n")
	files["lycaon/internal/db/migrations/registry.go"] = []byte("package migrations\n")
	files["lycaon/internal/extpacks/manifest.go"] = []byte(`package extpacks; const ExtensionAPIVersion="1.1.0"`)
	facts, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "config/packs/painted-wolf/platform/host/anchors/oar-profile.yaml"))
	testutil.FailErr(t, "read valid host fact vocabulary", err)
	files[capabilityPath] = facts
	files["schemas/guidance_registry.json"] = []byte(`{"hint_codes":{}}`)
	for rel, raw := range publicVocabularyFixture() {
		files[rel] = raw
	}
	files[securityPack+"/extension.yaml"] = []byte("manifest_version: 1\nid: painted-wolf/security-survey\nname: Security survey\nversion: 1.0.0\ncompatibility:\n  extension_api: '^1.1.0'\n")
	writeReleaseTree(t, repo, files)
	gittest.InitCommit(t, repo, "released workflow")
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), repo, []string{"--check-all"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "no stable") {
		t.Fatalf("missing baseline exit=%d: %s", code, &stderr)
	}
	for _, tag := range []string{"v0.9.0", "v1.0.1-rc.1", "v1.0.1"} {
		gittest.Run(t, repo, "tag", tag)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(t.Context(), repo, []string{"--check-all"}, &stdout, &stderr); code != 0 {
		t.Fatalf("stable check exit=%d: %s", code, &stderr)
	}
	report := stdout.String()
	if !strings.Contains(report, "v1.0.1 ") || !strings.Contains(report, "security-survey/security-survey") || strings.Contains(report, "Stable baseline: v0.9.0") || strings.Contains(report, "Stable baseline: v1.0.1-rc.1") {
		t.Fatalf("wrong baseline discovery: %s", report)
	}
	if code := run(t.Context(), repo, []string{"--check-all", "--published-versions", `["1.0.1"]`}, &stdout, &stderr); code != 0 {
		t.Fatalf("published migration check exit=%d: %s", code, &stderr)
	}
	for _, raw := range []string{"null", "{}", `["0.9.0"]`, `["invalid"]`, `["1.0.2"]`} {
		if code := run(t.Context(), repo, []string{"--check-all", "--published-versions", raw}, &stdout, &stderr); code != 1 {
			t.Fatalf("invalid published sources %s exit=%d", raw, code)
		}
	}
	if code := run(t.Context(), repo, []string{"--check-all", "--check"}, &stdout, &stderr); code != 2 {
		t.Fatalf("conflicting flags exit=%d", code)
	}
	testutil.FailErr(t, "change candidate guidance", os.WriteFile(filepath.Join(repo, filepath.FromSlash(securityPack), "guidance", "coordinator-security-plan.md"), []byte("drift\n"), 0o600))
	stderr.Reset()
	if code := run(t.Context(), repo, []string{"--check-all"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "without a version bump") {
		t.Fatalf("drift exit=%d: %s", code, &stderr)
	}
}

func TestWorkflowPathDiscovery(t *testing.T) {
	for _, rel := range []string{"", "other/workflows/a/workflow.yaml", packsRoot + "/acme/kit/workflows/_templates/a/workflow.yaml", packsRoot + "/acme/kit/archive/a/1.0.0/workflow.yaml"} {
		if _, _, ok := workflowPath(rel); ok {
			t.Fatalf("not a live workflow: %s", rel)
		}
	}
	pack, id, ok := workflowPath(packsRoot + "/acme/kit/workflows/repair/workflow.yaml")
	if !ok || pack != packsRoot+"/acme/kit" || id != "repair" {
		t.Fatalf("workflow path: %s %s %v", pack, id, ok)
	}
}

func TestCheckBundledReleaseCompatibilityFromStablePatch(t *testing.T) {
	root := configlayout.FindModuleRoot()
	if root == "" {
		t.Fatal("module root not found")
	}
	var report bytes.Buffer
	testutil.FailErr(t, "check bundled release contracts", checkReleaseCompatibility(t.Context(), filepath.Dir(root), "v1.0.1", &report))
	if !strings.Contains(report.String(), "Stable baseline: v1.0.1 ") {
		t.Fatalf("missing released baseline: %s", &report)
	}
}
