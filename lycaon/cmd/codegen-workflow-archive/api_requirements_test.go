package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWorkflowCapabilitiesRequireTheirAPIEpoch(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		archive    bool
	}{
		{name: "archive", archive: true, body: "id: example\n"},
		{name: "report retries", body: "controls:\n  report:\n    retries: 4\n"},
		{name: "phase retries", body: "phases:\n  - controls:\n      retries: 4\n"},
		{name: "task charter", body: "phases:\n  - fanout:\n      require_task_charter: false\n"},
		{name: "inventory accounting", body: "phases:\n  - review_loop:\n      require_inventory_accounted: true\n"},
		{name: "assignment binding", body: "phases:\n  - review_loop:\n      assignment_binding: explicit\n"},
		{name: "coverage reviewers", body: "phases:\n  - review_loop:\n      coverage_reviewers: [skeptic]\n"},
		{name: "followup attempts", body: "phases:\n  - review_loop:\n      followup_attempts: 2\n"},
		{name: "retirement", body: "retired: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "workflows", "example", "workflow.yaml")
			testutil.FailErr(t, "create workflow", os.MkdirAll(filepath.Dir(file), 0o750))
			testutil.FailErr(t, "write workflow", os.WriteFile(file, []byte(tc.body), 0o600))
			if tc.archive {
				testutil.FailErr(t, "create archive", os.Mkdir(filepath.Join(root, "archive"), 0o750))
			}
			for _, api := range []string{"^1.0.0", "^1.1.0"} {
				manifest := extpacks.Manifest{ID: "acme/kit", Compatibility: extpacks.ManifestCompatibility{ExtensionAPI: api}}
				err := checkPackAPI(root, manifest)
				if api == "^1.1.0" {
					testutil.FailErr(t, "check sufficient API", err)
				} else if err == nil || !strings.Contains(err.Error(), "needs extension API 1.1.0") {
					t.Fatalf("unsupported API admitted: %v", err)
				}
			}
		})
	}
}

func TestPackAPICheckRefusesInvalidInputs(t *testing.T) {
	root := t.TempDir()
	var report bytes.Buffer
	if err := checkPackAPIs(root, &report); err == nil {
		t.Fatal("missing pack tree passed")
	}
	pack := filepath.Join(root, filepath.FromSlash(packsRoot), "acme", "kit")
	testutil.FailErr(t, "create pack", os.MkdirAll(pack, 0o750))
	apiSource := filepath.Join(root, "lycaon", "internal", "extpacks", "manifest.go")
	testutil.FailErr(t, "create API source", os.MkdirAll(filepath.Dir(apiSource), 0o750))
	testutil.FailErr(t, "write API source", os.WriteFile(apiSource, []byte(`package extpacks; const ExtensionAPIVersion="1.1.0"`), 0o600))
	manifest := "manifest_version: 1\nid: acme/kit\nname: Kit\nversion: 1.0.0\ncompatibility:\n  extension_api: '^99.0.0'\n"
	file := filepath.Join(pack, "extension.yaml")
	testutil.FailErr(t, "write incompatible manifest", os.WriteFile(file, []byte(manifest), 0o600))
	if err := checkPackAPIs(root, &report); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("host mismatch: %v", err)
	}
	testutil.FailErr(t, "write compatible manifest", os.WriteFile(file, []byte(strings.ReplaceAll(manifest, "^99.0.0", "^1.0.0")), 0o600))
	testutil.FailErr(t, "check ordinary pack", checkPackAPIs(root, &report))
	testutil.FailErr(t, "create pack symlink", os.Symlink(file, filepath.Join(pack, "alias.yaml")))
	if err := checkPackAPIs(root, &report); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink admitted: %v", err)
	}
}

func TestCandidateExtensionAPIComesFromItsOwnSource(t *testing.T) {
	const rel = "lycaon/internal/extpacks/manifest.go"
	for _, tc := range []struct{ source, version string }{
		{`package extpacks; const ExtensionAPIVersion="1.0.0"`, "1.0.0"},
		{`package extpacks; const (Other="x"; ExtensionAPIVersion="2.0.0")`, "2.0.0"},
		{`package extpacks`, ""},
		{`package extpacks; const ExtensionAPIVersion=42`, ""},
		{`package extpacks; const ExtensionAPIVersion=other`, ""},
		{`invalid Go`, ""},
	} {
		version, err := sourceExtensionAPI(treeRelease{rel: []byte(tc.source)})
		if tc.version == "" {
			if err == nil {
				t.Fatalf("invalid API source admitted: %s", tc.source)
			}
			continue
		}
		testutil.FailErr(t, "read candidate API", err)
		if version.String() != tc.version {
			t.Fatalf("API=%s, want %s", version, tc.version)
		}
	}
}
