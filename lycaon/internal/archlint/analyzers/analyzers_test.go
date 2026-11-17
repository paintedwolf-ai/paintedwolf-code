package analyzers_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/lycaon/lycaon/internal/archlint/analyzers"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReexport(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzers.Reexport, "forward")
}

func TestSelfEmbed(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzers.SelfEmbed, "split")
}

func TestCopyDrop(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), analyzers.CopyDrop, "copies")
}

func TestIdentLit(t *testing.T) {
	for flag, value := range map[string]string{"identity-fields": "ident/owner.Ref.ID", "identity-owner": "ident/owner"} {
		if err := analyzers.IdentLit.Flags.Set(flag, value); err != nil {
			testutil.FailErr(t, "analyzers.IdentLit.Flags.Set failed", err)
		}
	}
	analysistest.Run(t, analysistest.TestData(), analyzers.IdentLit, "ident/use")
}

func TestIdentLitRefusesMalformedFields(t *testing.T) {
	for _, bad := range []string{"ID", "Ref.ID"} {
		if err := analyzers.IdentLit.Flags.Set("identity-fields", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTestImport(t *testing.T) {
	if err := analyzers.TestImport.Flags.Set("test-support", `test$`); err != nil {
		testutil.FailErr(t, "analyzers.TestImport.Flags.Set failed", err)
	}
	analysistest.Run(t, analysistest.TestData(), analyzers.TestImport, "prod", "prod/fixturetest")
}
