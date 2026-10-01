package rules_test

import (
	"github.com/lycaon/lycaon/internal/configlayout"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateRuleFile_valid(t *testing.T) {
	path := filepath.Join("testdata", "valid.yaml")
	if err := rules.ValidateRuleFile(path); err != nil {
		testutil.FailErr(t, "rules.ValidateRuleFile failed", err)
	}
}

func TestValidateRuleFile_rejectsBadID(t *testing.T) {
	path := filepath.Join("testdata", "bad-id.yaml")
	err := rules.ValidateRuleFile(path)
	if err == nil {
		t.Fatal("expected error for bad id prefix")
	}
}

func TestValidateRuleFile_rejectsInfoSeverity(t *testing.T) {
	path := filepath.Join("testdata", "info-severity.yaml")
	err := rules.ValidateRuleFile(path)
	if err == nil {
		t.Fatal("expected error for INFO severity")
	}
}

func TestValidateRuleFile_rejectsMissingLanguages(t *testing.T) {
	path := filepath.Join("testdata", "missing-languages.yaml")
	err := rules.ValidateRuleFile(path)
	if err == nil {
		t.Fatal("expected error for missing languages")
	}
}

func TestActiveRulesStructural(t *testing.T) {
	root := configlayout.FindModuleRoot()
	gates, err := rules.LoadOpengrepGates()
	testutil.FailErr(t, "load gates", err)
	for _, path := range gates.Lycaon {
		testutil.FailErr(t, "validate "+path, rules.ValidateRuleFile(filepath.Join(root, path)))
	}
	_, err = rules.CompileGateRules(gates, root)
	testutil.FailErr(t, "compile selection", err)
}
