package contract

import (
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// The structural rejects grep and code_rewrite return resolve to shipped copy
// selectable for both tools, so neither surfaces a bare code.
func TestStructuralIntelligenceStructuralHintsPointAtStructuralTools(t *testing.T) {
	t.Parallel()
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)
	for _, rejectErr := range []error{
		sourceview.PatternInvalid("a.go", errors.New("bad pattern")),
		sourceview.LanguageUnknown("a.go", "not-a-grammar"),
	} {
		var reject *toolrejection.ToolReject
		if !errors.As(rejectErr, &reject) || reject.Code == "" {
			t.Fatalf("structural reject = %T %v, want a coded tool reject", rejectErr, rejectErr)
		}
		entry, ok := cfg.HintCodes[reject.Code]
		if !ok {
			t.Fatalf("reject %q has no shipped hint", reject.Code)
		}
		view := guidance.NormalizeRejectCodeView(reject.Code, entry)
		if view.What == view.ID || (view.Fix == "" && view.Instead == "") {
			t.Fatalf("reject %q renders bare copy: %+v", reject.Code, view)
		}
		selectable := append(append([]string(nil), entry.Tools...), entry.Operations()...)
		for _, tool := range []string{"grep", "code_rewrite"} {
			if !slices.Contains(selectable, tool) {
				t.Errorf("reject %q copy is not selectable for %s (selects %v)", reject.Code, tool, selectable)
			}
		}
	}
}
