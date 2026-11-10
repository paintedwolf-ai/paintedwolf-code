package oarcopy

import (
	"slices"
	"testing"
)

func TestSyntaxRecoveryTracksTheCommandCatalog(t *testing.T) {
	got := syntaxOverrideCommands("command")
	for _, name := range []string{"tee", "edit", "replace-lines", "code-rewrite"} {
		if !slices.Contains(got, name) {
			t.Fatalf("missing executable remedy %s: %v", name, got)
		}
	}
	if got := syntaxOverrideCommands("edit"); len(got) != 0 {
		t.Fatalf("structured invocation got command guidance: %v", got)
	}
}
