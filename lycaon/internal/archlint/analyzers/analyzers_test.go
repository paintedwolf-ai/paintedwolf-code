package analyzers_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/lycaon/lycaon/internal/archlint/analyzers"
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
		t.Fatal(err)
	}
	analysistest.Run(t, analysistest.TestData(), analyzers.TestImport, "prod", "prod/fixturetest")
}
