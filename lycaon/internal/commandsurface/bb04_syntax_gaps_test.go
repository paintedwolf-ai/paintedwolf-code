package commandsurface_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/commandsurface/commandsurfacetest"
)

// TestBB04ExcludedSyntaxGapRefusalWitnesses closes the three excluded-form
// witness gaps carried from BB01 into BB04 (decisions/BB01-899b51c5-supplement.md):
//   - coproc (M01)
//   - extended globs (M01)
//   - fd allocation variables (M04)
//
// Each witness verifies whole-unit refusal (nil plan, zero admitted syntax or
// effects) with a typed COMMAND_SYNTAX_UNSUPPORTED diagnostic and valid span.
func TestBB04ExcludedSyntaxGapRefusalWitnesses(t *testing.T) {
	cases := []struct {
		name     string
		category string
		source   string
		afterSep bool
	}{
		// M01: coproc
		{
			name:     "coproc simple command",
			category: "M01/coproc",
			source:   "coproc sleep 1",
		},
		{
			name:     "coproc named compound",
			category: "M01/coproc",
			source:   "coproc worker { printf hi; }",
		},
		{
			name:     "coproc after preceding mutation refuses whole unit",
			category: "M01/coproc",
			source:   "printf mutated > ./sentinel; coproc sleep 1",
			afterSep: true,
		},

		// M01: extended globs
		{
			name:     "extglob negation",
			category: "M01/extglob",
			source:   "echo !(foo)",
		},
		{
			name:     "extglob alternation",
			category: "M01/extglob",
			source:   "echo @(a|b)",
		},
		{
			name:     "extglob zero-or-one",
			category: "M01/extglob",
			source:   "echo ?(a|b)",
		},
		{
			name:     "extglob one-or-more",
			category: "M01/extglob",
			source:   "echo +(a|b)",
		},
		{
			name:     "extglob zero-or-more",
			category: "M01/extglob",
			source:   "echo *(a|b)",
		},
		{
			name:     "extglob after preceding mutation refuses whole unit",
			category: "M01/extglob",
			source:   "printf mutated > ./sentinel; echo !(foo)",
			afterSep: true,
		},

		// M04: fd allocation variables
		{
			name:     "fd allocation output redirect on cat",
			category: "M04/fd-alloc-var",
			source:   "cat {fd}> ./out",
		},
		{
			name:     "fd allocation append redirect on cat",
			category: "M04/fd-alloc-var",
			source:   "cat {fd}>> ./out",
		},
		{
			name:     "fd allocation input redirect on cat",
			category: "M04/fd-alloc-var",
			source:   "cat {fd}< ./in",
		},
		{
			name:     "fd allocation via exec",
			category: "M04/fd-alloc-var",
			source:   "exec {fd}> ./out",
		},
		{
			name:     "fd allocation after preceding mutation refuses whole unit",
			category: "M04/fd-alloc-var",
			source:   "printf mutated > ./sentinel; cat {fd}> ./out",
			afterSep: true,
		},
		{
			name:     "exec fd allocation after preceding mutation refuses whole unit",
			category: "M04/fd-alloc-var",
			source:   "printf mutated > ./sentinel; exec {fd}> ./out",
			afterSep: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.category+"/"+tc.name, func(t *testing.T) {
			plan, err := commandsurfacetest.Admit(t, []byte(tc.source), commandsurface.ParseOptions{SourceName: "bb04-gap.pw"})
			if plan != nil {
				t.Fatalf("excluded form %q admitted a non-nil plan: %#v", tc.source, plan.View(t))
			}
			var diag *commandsurface.SyntaxDiagnostic
			if !errors.As(err, &diag) {
				t.Fatalf("expected *SyntaxDiagnostic for %q, got %T (%v)", tc.source, err, err)
			}
			if diag.Code != "COMMAND_SYNTAX_UNSUPPORTED" {
				t.Fatalf("diagnostic code = %q, want COMMAND_SYNTAX_UNSUPPORTED (diag=%+v)", diag.Code, diag)
			}
			if diag.Span.Line < 1 || diag.Span.Column < 1 || diag.Expected == "" {
				t.Fatalf("incomplete syntax diagnostic for %q: %+v", tc.source, diag)
			}
			if tc.afterSep {
				sep := strings.Index(tc.source, ";")
				if diag.Span.StartByte < sep {
					t.Fatalf("whole-unit refusal span %+v should point at the offending form after byte %d", diag.Span, sep)
				}
			}
		})
	}
}

// TestBB04ExcludedSyntaxGapLiteralControls verifies that quoted literal data
// resembling the excluded forms and ordinary numbered descriptors 0..9 remain
// admitted without false-positive refusals.
func TestBB04ExcludedSyntaxGapLiteralControls(t *testing.T) {
	controls := []string{
		`printf '%s\n' 'coproc sleep 1'`,
		`echo '!(foo)' "@(a|b)" '?(a|b)' '+(a|b)' '*(a|b)'`,
		`printf '%s\n' 'exec {fd}>out'`,
		`cat 3> ./out`,
		`cat 9>> ./out`,
	}
	for _, src := range controls {
		t.Run(src, func(t *testing.T) {
			plan, err := commandsurfacetest.Admit(t, []byte(src), commandsurface.ParseOptions{SourceName: "bb04-control.pw"})
			if err != nil || plan == nil {
				t.Fatalf("control form %q refused: plan=%v err=%v", src, plan, err)
			}
		})
	}
}
