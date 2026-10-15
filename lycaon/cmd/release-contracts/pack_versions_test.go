package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPackVersionsAndReleasedArchivesAreImmutable(t *testing.T) {
	pack := packsRoot + "/acme/kit"
	manifest := []byte("manifest_version: 1\nid: acme/kit\nname: Kit\nversion: 1.0.0\ncompatibility:\n  extension_api: '^1.0.0'\ndependencies:\n  painted-wolf/platform:\n    version: '^1.0.0'\n")
	for _, tc := range []struct{ name, version, change, want string }{
		{name: "unchanged", version: "1.0.0"},
		{name: "ignored OS metadata", version: "1.0.0", change: "ds_store"},
		{name: "changed bytes", version: "1.0.0", change: "body", want: "without a version bump"},
		{name: "added bytes", version: "1.0.0", change: "add", want: "without a version bump"},
		{name: "removed bytes", version: "1.0.0", change: "remove", want: "without a version bump"},
		{name: "version advance", version: "1.0.1", change: "body"},
		{name: "downgrade", version: "0.9.0", want: "older than released"},
		{name: "rewritten archive", version: "1.0.1", change: "archive", want: "released archive changed"},
		{name: "added file in released archive", version: "1.0.1", change: "archive_add", want: "released archive changed"},
		{name: "new fact with patch bump", version: "1.0.1", change: "new_fact", want: "without a minor version bump"},
		{name: "new fact with unraised floor", version: "1.1.0", change: "new_fact", want: "extension_api"},
		{name: "new fact with raised floors", version: "1.1.0", change: "new_fact_raised"},
		{name: "missing pack", version: "1.0.0", change: "manifest", want: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := treeRelease{
				capabilityPath:                                         []byte("host_facts:\n - name: baseline_fact\n   type: bool\n"),
				"lycaon/internal/extpacks/manifest.go":                 []byte(`package extpacks; const ExtensionAPIVersion="1.0.0"`),
				packsRoot + "/painted-wolf/platform/extension.yaml":    []byte("manifest_version: 1\nid: painted-wolf/platform\nname: Platform\nversion: 1.0.0\ncompatibility:\n  extension_api: '^1.0.0'\n"),
				pack + "/extension.yaml":                               manifest,
				pack + "/guidance/example.md":                          []byte("released\n"),
				pack + "/archive/example/1.0.0/workflow.yaml":          []byte("sealed\n"),
			}
			var paths []string
			for path := range source {
				if strings.HasPrefix(path, pack+"/") {
					paths = append(paths, path)
				}
			}
			repo := t.TempDir()
			writeReleaseTree(t, repo, source)
			current := bytes.ReplaceAll(manifest, []byte("version: 1.0.0"), []byte("version: "+tc.version))
			if tc.change == "new_fact_raised" {
				current = bytes.ReplaceAll(current, []byte("'^1.0.0'"), []byte("'^1.1.0'"))
			}
			file := filepath.Join(repo, filepath.FromSlash(pack), "extension.yaml")
			testutil.FailErr(t, "write candidate version", os.WriteFile(file, current, 0o600))
			switch tc.change {
			case "ds_store":
				testutil.FailErr(t, "write OS metadata", os.WriteFile(filepath.Join(repo, filepath.FromSlash(pack), ".DS_Store"), []byte("ignored\n"), 0o600))
			case "body":
				testutil.FailErr(t, "change pack body", os.WriteFile(filepath.Join(repo, filepath.FromSlash(pack), "guidance/example.md"), []byte("changed\n"), 0o600))
			case "add":
				testutil.FailErr(t, "add pack file", os.WriteFile(filepath.Join(repo, filepath.FromSlash(pack), "new.md"), []byte("new\n"), 0o600))
			case "remove":
				testutil.FailErr(t, "remove pack body", os.Remove(filepath.Join(repo, filepath.FromSlash(pack), "guidance/example.md")))
			case "archive":
				testutil.FailErr(t, "alter released archive", os.WriteFile(filepath.Join(repo, filepath.FromSlash(pack), "archive/example/1.0.0/workflow.yaml"), []byte("changed\n"), 0o600))
			case "archive_add":
				testutil.FailErr(t, "add file to released archive", os.WriteFile(filepath.Join(repo, filepath.FromSlash(pack), "archive/example/1.0.0/extra.md"), []byte("extra\n"), 0o600))
			case "new_fact", "new_fact_raised":
				policyDir := filepath.Join(repo, filepath.FromSlash(pack), "policy")
				testutil.FailErr(t, "create policy dir", os.MkdirAll(policyDir, 0o750))
				testutil.FailErr(t, "write policy fact requirement", os.WriteFile(filepath.Join(policyDir, "rules.yaml"), []byte("requires:\n  facts: [added_fact]\n"), 0o600))
			case "manifest":
				testutil.FailErr(t, "remove manifest", os.Remove(file))
			}
			err := checkReleasedPack(source, repo, pack, paths)
			if tc.want == "" {
				testutil.FailErr(t, "check pack version", err)
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("check=%v, want %s", err, tc.want)
			}
		})
	}
}

func TestArchiveCapturesGuidanceFromAnotherProvider(t *testing.T) {
	source := committedRelease(t)
	rel := securityPack + "/guidance/coordinator-security-plan.md"
	body := source[rel]
	delete(source, rel)
	source[packsRoot+"/painted-wolf/platform/guidance/coordinator-security-plan.md"] = body
	target := t.TempDir()
	_, err := seal(source, securityPack, "security-survey", target, false)
	testutil.FailErr(t, "seal shared guidance", err)
	_, err = seal(source, securityPack, "security-survey", target, true)
	testutil.FailErr(t, "check sealed shared guidance", err)
	source[rel] = body
	if _, _, err := sealedFiles(source, securityPack, "security-survey"); err == nil {
		t.Fatal("ambiguous guidance was sealed")
	}
}
