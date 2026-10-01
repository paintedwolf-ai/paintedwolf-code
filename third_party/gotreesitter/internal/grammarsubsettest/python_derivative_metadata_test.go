//go:build grammar_subset && !grammar_subset_python && (grammar_subset_bitbake || grammar_subset_mojo || grammar_subset_starlark)

package grammarsubsettest

import (
	"testing"

	"github.com/odvcencio/gotreesitter/grammars"
)

func TestPythonDerivativeSubsetDoesNotRegisterPythonMetadata(t *testing.T) {
	if _, ok := grammars.LookupExternalScannerSpec("python"); ok {
		t.Fatal("python scanner metadata is present in a Python-derivative subset")
	}
}
