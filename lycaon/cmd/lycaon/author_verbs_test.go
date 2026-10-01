package main

import (
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRulesTestFlags(t *testing.T) {
	got, err := parseRulesTestFlags([]string{"--json", "--project", "/tmp/p", "policy", "conformance/a.json"})
	if err != nil {
		t.Fatalf("parseRulesTestFlags: %v", err)
	}
	if !got.jsonOut || got.projectDir != "/tmp/p" {
		t.Fatalf("flags = %+v", got)
	}
	if len(got.paths) != 2 || got.paths[0] != "policy" {
		t.Fatalf("paths = %v", got.paths)
	}
	if _, err := parseRulesTestFlags([]string{"--nope"}); err == nil {
		t.Fatal("expected unknown-flag error")
	}
	if _, err := parseRulesTestFlags([]string{"--project"}); err == nil {
		t.Fatal("expected missing-value error")
	}
}

func TestParsePromptsRenderFlags(t *testing.T) {
	got, err := parsePromptsRenderFlags([]string{"partials/x.md", "--var", "focus=triage", "--var", "role=impl", "--check"})
	if err != nil {
		t.Fatalf("parsePromptsRenderFlags: %v", err)
	}
	if got.ref != "partials/x.md" || !got.check {
		t.Fatalf("flags = %+v", got)
	}
	if got.vars["focus"] != "triage" || got.vars["role"] != "impl" {
		t.Fatalf("vars = %v", got.vars)
	}
	if _, err := parsePromptsRenderFlags([]string{"--var", "novalue"}); err == nil {
		t.Fatal("expected k=v error")
	}
	if _, err := parsePromptsRenderFlags([]string{"a.md", "b.md"}); err == nil {
		t.Fatal("expected single-ref error")
	}
}

// TestDefaultRulePathsFindsPackPolicy pins the no-argument scope an author gets
// while standing in the directory that holds their pack.
func TestDefaultRulePathsFindsPackPolicy(t *testing.T) {
	root := t.TempDir()
	pack := filepath.Join(root, "triage-lite")
	if err := os.MkdirAll(filepath.Join(pack, "policy"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pack, "extension.yaml"), []byte("id: acme/triage-lite\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "not-a-pack", "policy"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got := defaultRulePaths(root)
	if len(got) != 1 || !strings.HasSuffix(got[0], filepath.Join("triage-lite", "policy")) {
		t.Fatalf("default paths = %v", got)
	}
}

func TestDefaultRulePathsFindsProjectOverlay(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, settingsoverlay.DirName(), "policy"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got := defaultRulePaths(root)
	if len(got) != 1 || !strings.HasSuffix(got[0], filepath.Join(settingsoverlay.DirName(), "policy")) {
		t.Fatalf("default paths = %v", got)
	}
}
