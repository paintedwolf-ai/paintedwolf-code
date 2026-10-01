package grammargen

import "testing"

func TestSwiftABIManglingGrammar(t *testing.T) {
	g := SwiftABIManglingGrammar()
	if warnings := Validate(g); len(warnings) != 0 {
		t.Fatalf("SwiftABIManglingGrammar validation warnings: %v", warnings)
	}
	if err := RunTests(g); err != nil {
		t.Fatalf("SwiftABIManglingGrammar tests failed: %v", err)
	}
}
