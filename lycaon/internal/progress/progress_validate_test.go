package progress_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
)

func TestValidateAuthorProgress_acceptsOverlongLabel(t *testing.T) {
	long := strings.Repeat("b", progress.MaxLabelRunes+3)
	if code, _, ok := progress.ValidateAuthorProgress("", "- [ ] "+long); !ok {
		t.Fatalf("over-long labels must be accepted, got reject %q", code)
	}
	step := progress.DeriveProgress("- [ ] "+long, 0)
	if len(step.Items) != 1 {
		t.Fatalf("items = %d want 1", len(step.Items))
	}
	if n := len([]rune(step.Items[0].Label)); n > progress.MaxLabelRunes+1 {
		t.Fatalf("derived label not clamped: %d runes", n)
	}
}

func TestPlanChecklistLineCount(t *testing.T) {
	content := strings.Repeat("- [ ] step\n", progress.MaxAuthorProgressLines)
	if progress.PlanChecklistLineCount(content) != progress.MaxAuthorProgressLines {
		t.Fatalf("count = %d want %d", progress.PlanChecklistLineCount(content), progress.MaxAuthorProgressLines)
	}
	if code, _, ok := progress.ValidateAuthorProgress("", content+"- [ ] one more\n"); ok || code != "PROGRESS_TOO_MANY_LINES" {
		t.Fatalf("ValidateAuthorProgress = %q ok=%v want PROGRESS_TOO_MANY_LINES", code, ok)
	}
}

func TestValidateAuthorProgress_noOpenStep(t *testing.T) {
	if code, _, ok := progress.ValidateAuthorProgress("", "## Progress\n- [x] done\n"); ok || code != "PROGRESS_NO_OPEN_STEP" {
		t.Fatalf("initial all-done plan must reject with PROGRESS_NO_OPEN_STEP, got %q ok=%v", code, ok)
	}
	if code, _, ok := progress.ValidateAuthorProgress("", "## Progress\nno checklist items\n"); ok || code != "PROGRESS_NO_OPEN_STEP" {
		t.Fatalf("initial empty checklist must reject with PROGRESS_NO_OPEN_STEP, got %q ok=%v", code, ok)
	}
	if code, _, ok := progress.ValidateAuthorProgress("", "## Progress\n- [>] optional only\n"); ok || code != "PROGRESS_NO_OPEN_STEP" {
		t.Fatalf("initial optional-only checklist must reject with PROGRESS_NO_OPEN_STEP, got %q ok=%v", code, ok)
	}
	if code, _, ok := progress.ValidateAuthorProgress("", "## Progress\n- [ ] open step\n"); !ok {
		t.Fatalf("initial plan with open step must be accepted, got %q", code)
	}
	if code, _, ok := progress.ValidateAuthorProgress("## Progress\n- [ ] prior\n", "## Progress\n- [x] prior\n"); !ok {
		t.Fatalf("all-terminal update must be accepted when stored plan had items, got %q", code)
	}
}

func TestFirstNestedChecklistLine(t *testing.T) {
	_, found := progress.FirstNestedChecklistLine("- [ ] top\n  - [ ] nested\n")
	if !found {
		t.Fatal("expected nested checklist line")
	}
	if _, found := progress.FirstNestedChecklistLine("- [ ] top\n  prose paragraph\n"); found {
		t.Fatal("indented prose should not count")
	}
}

func TestFirstPendingAfterOptional(t *testing.T) {
	_, found := progress.FirstPendingAfterOptional("- [>] note\n- [ ] still open\n")
	if !found {
		t.Fatal("expected pending after optional")
	}
	if _, found := progress.FirstPendingAfterOptional("- [ ] open\n- [>] note\n"); found {
		t.Fatal("optional after pending should be allowed")
	}
	if _, found := progress.FirstPendingAfterOptional("- [>] note\n- [x] done\n"); found {
		t.Fatal("done after optional should be allowed")
	}
}
