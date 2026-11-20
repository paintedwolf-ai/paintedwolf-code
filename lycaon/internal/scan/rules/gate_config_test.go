package rules_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/scan/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
)

func TestActiveRulesStructural(t *testing.T) {
	root := configlayout.FindModuleRoot()
	gates, err := rules.LoadOpengrepGates()
	testutil.FailErr(t, "load gates", err)
	for _, path := range gates.Lycaon {
		testutil.FailErr(t, "validate "+path, scantest.ValidateRuleFile(filepath.Join(root, path)))
	}
	_, err = rules.CompileGateRules(gates, root)
	testutil.FailErr(t, "compile selection", err)
}
