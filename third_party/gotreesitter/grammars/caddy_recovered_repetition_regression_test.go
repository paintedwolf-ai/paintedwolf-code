//go:build !grammar_subset || grammar_subset_caddy

package grammars

import (
	"os"
	"path/filepath"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

func TestCaddyRecoveredStringLiteralRepetitionStaysBounded(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "testdata", "caddy", "security-header.Caddyfile"))
	if err != nil {
		t.Fatalf("read witness: %v", err)
	}
	lang := CaddyLanguage()
	if lang == nil {
		t.Fatal("CaddyLanguage returned nil")
	}

	matches := 0
	for _, policy := range lang.ConflictPolicies {
		if policy.State != 86 || policy.Lookahead != 25 {
			continue
		}
		matches++
		if policy.Kind != gotreesitter.ConflictPolicyRecoveredRepetitionReduce || len(policy.ReduceSymbols) != 1 || policy.ReduceSymbols[0] != 68 {
			t.Fatalf("Caddy certified policy = %+v", policy)
		}
	}
	if matches != 1 {
		t.Fatalf("Caddy certified row policies = %d, want 1", matches)
	}

	tree, err := gotreesitter.NewParser(lang).Parse(source)
	if err != nil {
		t.Fatalf("parse witness: %v", err)
	}
	if tree == nil {
		t.Fatal("parse returned nil tree")
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		t.Fatal("parse returned nil root")
	}
	if got, want := root.EndByte(), uint32(len(source)); got != want {
		t.Fatalf("root.EndByte = %d, want %d", got, want)
	}
	if !root.HasError() {
		t.Fatal("witness unexpectedly lost its C-matching recovery error")
	}
	runtime := tree.ParseRuntime()
	if runtime.NodesAllocated > 5000 {
		t.Fatalf("NodesAllocated = %d, want <= 5000 (%s)", runtime.NodesAllocated, runtime.Summary())
	}
	if runtime.MaxStacksSeen > 16 {
		t.Fatalf("MaxStacksSeen = %d, want <= 16 (%s)", runtime.MaxStacksSeen, runtime.Summary())
	}
}
