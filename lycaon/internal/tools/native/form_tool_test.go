package native

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolreach"
)

// Host quoting must keep expansions and apostrophes from reaching the parser as syntax.
func TestQuoteProgramKeepsValuesLiteral(t *testing.T) {
	got := toolreach.QuoteProgram([]string{"code-rewrite", "--pattern", "f($A, 'x')"})
	want := `code-rewrite --pattern 'f($A, '\''x'\'')'`
	if got != want {
		t.Fatalf("program = %s", got)
	}
}
