package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCandidateProjectRulesUseExplicitSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate.yaml")
	source := "rules:\n- id: candidate\n  languages: [javascript]\n  severity: ERROR\n  message: Candidate finding\n  pattern: sink(...)\n"
	testutil.FailErr(t, "write candidate", os.WriteFile(path, []byte(source), 0o600))
	bundle, err := loadProjectRules(path, t.TempDir())
	testutil.FailErr(t, "load candidate", err)
	testutil.FailErr(t, "validate candidate family", validateProjectFamilies([]projectCase{{Name: "candidate", Families: []string{"candidate"}}}, bundle))
	if err := validateProjectFamilies([]projectCase{{Name: "shipping", Families: []string{"lycaon.javascript.command-injection"}}}, bundle); err == nil {
		t.Fatal("candidate evaluation included unselected shipping rules")
	}
	testutil.FailErr(t, "remove candidate file", os.Remove(path))
	if _, err := loadProjectRules(path, t.TempDir()); err == nil {
		t.Fatal("missing candidate silently fell back to shipping rules")
	}
}
