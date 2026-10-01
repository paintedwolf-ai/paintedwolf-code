package standingpatterns_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/standingpatterns"
)

func writeStandingPatterns(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, settingsoverlay.DirName())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "standing-patterns.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestLoadMissingFileEmptyConfig(t *testing.T) {
	root := t.TempDir()
	cfg, err := standingpatterns.Load(t.Context(), root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Rules) != 0 || len(cfg.Skipped) != 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadValidRules(t *testing.T) {
	root := t.TempDir()
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
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules = %+v", cfg.Rules)
	}
	r := cfg.Rules[0]
	if r.ID != "go-panic" || r.Label != "panic" || r.Pattern != "panic($A)" {
		t.Fatalf("rule = %+v", r)
	}
	if len(r.Langs) != 1 || r.Langs[0] != "go" {
		t.Fatalf("langs = %v", r.Langs)
	}
	if len(r.PathScope) != 1 || r.PathScope[0] != "**/*.go" {
		t.Fatalf("path_scope = %v", r.PathScope)
	}
}

func TestLoadSkipsInvalidRules(t *testing.T) {
	root := t.TempDir()
	writeStandingPatterns(t, root, `
patterns:
  - id: bad-syntax
    label: bad
    pattern: "[[["
    langs: [go]
  - id: good
    label: fmt.Println
    pattern: fmt.Println($A)
    langs: [go]
`)
	cfg, err := standingpatterns.Load(t.Context(), root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].ID != "good" {
		t.Fatalf("rules = %+v", cfg.Rules)
	}
	if len(cfg.Skipped) != 1 || cfg.Skipped[0].ID != "bad-syntax" {
		t.Fatalf("skipped = %+v", cfg.Skipped)
	}
}

func TestLoadRequiresFields(t *testing.T) {
	root := t.TempDir()
	writeStandingPatterns(t, root, `
patterns:
  - id: ""
    label: x
    pattern: panic($A)
  - id: no-label
    pattern: panic($A)
  - id: no-pattern
    label: x
`)
	cfg, err := standingpatterns.Load(t.Context(), root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Rules) != 0 {
		t.Fatalf("rules = %+v", cfg.Rules)
	}
	if len(cfg.Skipped) != 3 {
		t.Fatalf("skipped = %+v", cfg.Skipped)
	}
}

func TestLoadDuplicateIDSkipped(t *testing.T) {
	root := t.TempDir()
	writeStandingPatterns(t, root, `
patterns:
  - id: dup
    label: one
    pattern: panic($A)
    langs: [go]
  - id: dup
    label: two
    pattern: fmt.Println($A)
    langs: [go]
`)
	cfg, err := standingpatterns.Load(t.Context(), root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules = %+v", cfg.Rules)
	}
	if len(cfg.Skipped) != 1 || !strings.Contains(cfg.Skipped[0].Reason, "duplicate") {
		t.Fatalf("skipped = %+v", cfg.Skipped)
	}
}
