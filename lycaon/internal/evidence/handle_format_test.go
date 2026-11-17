package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

// Handle kinds are open lowercase strings; the grammar is not a kind list.
func TestHandleGrammarAcceptsOpenKinds(t *testing.T) {
	for _, kind := range []string{"read", "mykind", "custom"} {
		handle := evidence.FormatHandle(kind, 1)
		if !evidence.HandleGrammar.MatchString(handle) {
			t.Fatalf("HandleGrammar rejected open kind %q handle %q", kind, handle)
		}
	}
	for _, handle := range []string{"BadKind#1", "read#", "#1"} {
		if evidence.HandleGrammar.MatchString(handle) {
			t.Fatalf("HandleGrammar accepted %q", handle)
		}
	}
}
