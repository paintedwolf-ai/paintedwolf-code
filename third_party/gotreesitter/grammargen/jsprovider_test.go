package grammargen

import (
	"github.com/odvcencio/gotreesitter/grammars"
)

// Register the JavaScript grammar provider for tests that exercise
// ImportGrammarJS. This lives in a _test.go file so production grammargen stays
// free of the grammars registry (~200 grammars); only the test binary links it.
func init() {
	SetJSGrammarProvider(grammars.JavascriptLanguage)
}
