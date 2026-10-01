package standingpatterns_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/standingpatterns"
)

func TestCountFlagsMatchesAndPathScope(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() { panic(\"x\") }\n"), 0o644); err != nil {
		t.Fatalf("write go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("panic(\"x\")\n"), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}
	writeStandingPatterns(t, root, `
patterns:
  - id: go-panic
    label: panic
    pattern: panic($A)
    langs: [go]
    path_scope: ["**/*.go"]
`)
	cfg, err := standingpatterns.Load(t.Context(), root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	flags := standingpatterns.CountFlags(t.Context(), root, cfg.Rules)
	if len(flags) != 1 {
		t.Fatalf("flags = %+v", flags)
	}
	if flags[0].Label != "panic" || flags[0].Count != 1 {
		t.Fatalf("flag = %+v", flags[0])
	}
}

func TestCountFlagsOmitsZeroMatches(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write go: %v", err)
	}
	writeStandingPatterns(t, root, `
patterns:
  - id: go-panic
    label: panic
    pattern: panic($A)
    langs: [go]
`)
	cfg, err := standingpatterns.Load(t.Context(), root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if flags := standingpatterns.CountFlags(t.Context(), root, cfg.Rules); len(flags) != 0 {
		t.Fatalf("flags = %+v", flags)
	}
}
