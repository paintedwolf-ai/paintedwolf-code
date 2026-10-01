package diag_test

import (
	"strings"
	"testing"

	"github.com/odvcencio/gotreesitter/taproot/diag"
)

func TestRenderQuotesLine(t *testing.T) {
	src := []byte("let x = ;\nlet y = 1;")
	d := diag.Diagnostic{
		Code:     "ELIO0003",
		Severity: diag.SeverityError,
		Line:     1,
		Col:      9,
		Message:  "unexpected ';'",
		Hint:     "provide an expression",
	}
	out := diag.Render(src, d)

	// Must contain the code.
	if !strings.Contains(out, "ELIO0003") {
		t.Errorf("Render output missing code; got:\n%s", out)
	}
	// Must contain the source line text.
	if !strings.Contains(out, "let x = ;") {
		t.Errorf("Render output missing source line; got:\n%s", out)
	}
	// Must contain a caret indicator.
	if !strings.Contains(out, "^") {
		t.Errorf("Render output missing caret; got:\n%s", out)
	}
	// Must contain the message.
	if !strings.Contains(out, "unexpected ';'") {
		t.Errorf("Render output missing message; got:\n%s", out)
	}
	// Must contain the hint.
	if !strings.Contains(out, "provide an expression") {
		t.Errorf("Render output missing hint; got:\n%s", out)
	}
}

func TestRenderNoHint(t *testing.T) {
	src := []byte("bad source")
	d := diag.Diagnostic{
		Code:     "SEL0001",
		Severity: diag.SeverityError,
		Line:     1,
		Col:      1,
		Message:  "syntax error",
	}
	out := diag.Render(src, d)
	if !strings.Contains(out, "SEL0001") {
		t.Errorf("missing code in: %s", out)
	}
	if !strings.Contains(out, "bad source") {
		t.Errorf("missing source line in: %s", out)
	}
	// No hint section expected.
	if strings.Contains(out, "hint:") {
		t.Errorf("unexpected hint section in: %s", out)
	}
}

func TestRenderOutOfRangeLineGraceful(t *testing.T) {
	src := []byte("one line")
	d := diag.Diagnostic{
		Code:    "X0001",
		Line:    99, // beyond end of src
		Col:     1,
		Message: "oob",
	}
	// Must not panic, must still include message.
	out := diag.Render(src, d)
	if !strings.Contains(out, "oob") {
		t.Errorf("missing message in: %s", out)
	}
}
