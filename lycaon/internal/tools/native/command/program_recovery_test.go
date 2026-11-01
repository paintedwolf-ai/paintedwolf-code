package command_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func TestCommandRecoveryPreservesUnitAndPriorEffects(t *testing.T) {
	for _, body := range []string{"printf '%s' 'open", "if true; then echo ok", "cat <<'END'\nliteral\n"} {
		t.Run(body, func(t *testing.T) {
			f := newRecoveryFixture(t)
			_, err := f.call(t, t.Context(), "printf before > retained; "+body)
			rejected := toolrejection.AsToolReject(err)
			if rejected == nil || rejected.Code != "COMMAND_SYNTAX_INVALID" || rejected.Data["command_execution"] != "not_started" {
				t.Fatalf("top-level refusal: %#v / %v", rejected, err)
			}
			if _, err := os.Stat(filepath.Join(f.Dir, "retained")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid unit executed: %v", err)
			}
		})
	}
	f := newRecoveryFixture(t)
	testutil.FailErr(t, "set write progress", f.SetProgress("- [ ] Retain the write before inspecting source admission"))
	out, err := f.call(t, t.Context(), `printf before | tee retained; eval 'if'`)
	testutil.FailErr(t, "run captured invalid source", err)
	got := decodeOutcome(t, out)
	if got.Refusal == nil || got.Refusal.Details["diagnostic_top_level"] != false || got.Refusal.Details["command_execution"] != "observed" || strings.Contains(got.Refusal.Feedback, "Nothing executed") {
		t.Fatalf("captured refusal lost prior effects: %+v / %+v", got, got.Refusal)
	}
	retained, err := os.ReadFile(filepath.Join(f.Dir, "retained"))
	testutil.FailErr(t, "read retained effect", err)
	if string(retained) != "before" || got.CompletedEffects < 1 || got.Refusal.Details["parser_kind"] == nil {
		t.Fatalf("lost completed write or parser state: %+v / %q", got, retained)
	}
	if !strings.Contains(got.Stderr, "if") || strings.Contains(got.Stderr, "encoding") {
		t.Fatalf("shell diagnostic did not name parse problem: %q", got.Stderr)
	}
}

func TestCommandRecoveryKeepsNestedSourceIdentitiesDistinct(t *testing.T) {
	f := newRecoveryFixture(t)
	out, err := f.call(t, t.Context(), `eval 'eval "if"'`)
	testutil.FailErr(t, "run nested invalid source", err)
	got := decodeOutcome(t, out)
	if got.Refusal == nil {
		t.Fatalf("missing refusal: %+v", got)
	}
	facts := got.Refusal.Details
	primary, _ := facts["source_unit_id"].(string)
	enclosing, _ := facts["operation_source_unit_id"].(string)
	if primary == "" || enclosing == "" || primary == enclosing || enclosing == "submitted" || facts["diagnostic_top_level"] != false {
		t.Fatalf("nested source provenance collapsed: %#v", facts)
	}
}

func newRecoveryFixture(t *testing.T) *programFixture {
	t.Helper()
	f := newProgramFixture(t)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load recovery catalog", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	f.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	return f
}

func TestCommandInputIsExplicitAndPreservesLiteralBytes(t *testing.T) {
	f := newRecoveryFixture(t)
	for _, tc := range []struct{ source, want string }{
		{`if read -r value; then printf unexpected; else printf eof; fi`, "eof"},
		{"read -r value <<'INPUT'\nit's literal $HOME\nINPUT\nprintf '%s' \"$value\"", "it's literal $HOME"},
	} {
		out, err := f.call(t, t.Context(), tc.source)
		testutil.FailErr(t, "run explicit input", err)
		got := decodeOutcome(t, out)
		if got.Status != 0 || got.Refusal != nil || got.Stdout != tc.want {
			t.Fatalf("stdin contract changed: %+v, want %q", got, tc.want)
		}
	}
}
