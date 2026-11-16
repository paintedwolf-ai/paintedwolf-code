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

func TestTestImport(t *testing.T) {
	if err := analyzers.TestImport.Flags.Set("test-support", `test$`); err != nil {
		testutil.FailErr(t, "analyzers.TestImport.Flags.Set failed", err)
	}
	analysistest.Run(t, analysistest.TestData(), analyzers.TestImport, "prod", "prod/fixturetest")
}
