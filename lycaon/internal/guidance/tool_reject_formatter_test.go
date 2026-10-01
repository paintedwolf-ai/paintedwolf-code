package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestToolRejectFormatter_CompactRejectShape(t *testing.T) {
	setTestRenderer(t)
	dedup := NewFeedbackDeduper()
	f := NewToolRejectFormatter(dedup)
	out := denyOutcome{
		rejectCode: "SPEC_POSTURE_STUB_REQUIRED",
		phase:      "1",
		phaseName:  "Research depth (stub plan file)",
		min:        "Write a plan stub",
	}
	block, err := f.FormatBlock(t.Context(), "s1", "task", out, PlanProgress{
		ProgressChecklist: "[ ] Phase 1 — Research depth (stub plan file)\n",
		ChecklistHash:     "h1",
	}, phaseFixtureCopy())
	testutil.FailErr(t, "f.FormatBlock failed", err)
	for _, want := range []string{
		">>> Spec posture blocked",
		"Tool: task",
		"Blocked at: Phase 1 — Research depth (stub plan file)",
		"Cause:",
		"Fix:",
		"Progress:",
		"Code: SPEC_POSTURE_STUB_REQUIRED",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
	if !strings.Contains(block, "Details:") {
		t.Fatalf("expected Details on first block, got:\n%s", block)
	}
}

func TestFeedbackDedup_SecondRejectOmitsDetails(t *testing.T) {
	setTestRenderer(t)
	dedup := NewFeedbackDeduper()
	f := NewToolRejectFormatter(dedup)
	out := denyOutcome{
		rejectCode: "SPEC_POSTURE_STUB_REQUIRED",
		phase:      "1",
		phaseName:  "Research depth (stub plan file)",
		min:        "Write a plan stub",
	}
	progress := PlanProgress{
		ProgressChecklist: "[ ] Phase 1 — Research depth (stub plan file)\n",
		ChecklistHash:     "h1",
	}
	first, err := f.FormatBlock(t.Context(), "s1", "task", out, progress, phaseFixtureCopy())
	testutil.FailErr(t, "f.FormatBlock failed", err)
	second, err := f.FormatBlock(t.Context(), "s1", "task", out, progress, phaseFixtureCopy())
	testutil.FailErr(t, "f.FormatBlock failed", err)
	if !strings.Contains(first, "Details:") {
		t.Fatalf("expected first includes Details")
	}
	if strings.Contains(second, "Details:") {
		t.Fatalf("expected second omits Details, got:\n%s", second)
	}
}

func TestFeedbackDedup_VerboseAlwaysDetails(t *testing.T) {
	t.Setenv("LYCAON_TOOL_FEEDBACK_VERBOSE", "1")
	setTestRenderer(t)
	dedup := NewFeedbackDeduper()
	f := NewToolRejectFormatter(dedup)
	out := denyOutcome{
		rejectCode: "SPEC_POSTURE_STUB_REQUIRED",
		phase:      "1",
		phaseName:  "Research depth (stub plan file)",
		min:        "Write a plan stub",
	}
	progress := PlanProgress{
		ProgressChecklist: "[ ] Phase 1 — Research depth (stub plan file)\n",
		ChecklistHash:     "h1",
	}
	a, _ := f.FormatBlock(t.Context(), "s1", "task", out, progress, phaseFixtureCopy())
	b, _ := f.FormatBlock(t.Context(), "s1", "task", out, progress, phaseFixtureCopy())
	if !strings.Contains(a, "Details:") || !strings.Contains(b, "Details:") {
		t.Fatalf("expected verbose always includes Details")
	}
}

type denyOutcome struct {
	rejectCode string
	phase      string
	phaseName  string
	min        string
	max        string
}

func (o denyOutcome) GetRejectCode() string        { return o.rejectCode }
func (o denyOutcome) GetPhaseRequired() string     { return o.phase }
func (o denyOutcome) GetPhaseRequiredName() string { return o.phaseName }
func (o denyOutcome) GetMinRequired() string       { return o.min }
func (o denyOutcome) GetMaxPlaybook() string       { return o.max }

func TestToolRejectFormatterRetainsFrozenRecovery(t *testing.T) {
	setTestRenderer(t)
	f := NewToolRejectFormatter(NewFeedbackDeduper())
	out := denyOutcome{rejectCode: "SPEC_POSTURE_SCOPE_REQUIRED", phase: "0", phaseName: "Infer"}
	block, err := f.FormatBlock(t.Context(), "s1", "task", out, PlanProgress{NextAction: "add scope"}, phaseFixtureCopy())
	testutil.FailErr(t, "f.FormatBlock failed", err)
	if !strings.Contains(block, "Fix: recover {{ literal.path }}") {
		t.Fatalf("expected hint fix in:\n%s", block)
	}
}

func TestHostRejectCode(t *testing.T) {
	raw := ">>> Spec posture blocked\nCode: SPEC_POSTURE_STUB_REQUIRED\n"
	if got := HostRejectCode(raw); got != "SPEC_POSTURE_STUB_REQUIRED" {
		t.Fatalf("HostRejectCode = %q", got)
	}
}

func TestToolRejectFormatter_ProgressCompactFromChecklist(t *testing.T) {
	setTestRenderer(t)
	f := NewToolRejectFormatter(NewFeedbackDeduper())
	out := denyOutcome{
		rejectCode: "SPEC_POSTURE_DELEGATION_FORBIDDEN",
		phase:      "1",
		phaseName:  "Research depth (stub plan file)",
	}
	progress := PlanProgress{
		ProgressChecklist: "[x] Phase 0 — Infer\n[ ] Phase 1 — Research depth (stub plan file)\n",
		ChecklistHash:     "h2",
	}
	block, err := f.FormatBlock(t.Context(), "s1", "delegate_dispatch", out, progress, phaseFixtureCopy())
	testutil.FailErr(t, "f.FormatBlock failed", err)
	if !strings.Contains(block, "Progress:") || !strings.Contains(block, "Phase 1 — Research depth") {
		t.Fatalf("expected phase line in progress:\n%s", block)
	}
}

func TestNewToolRejectFormatter_NilDedupUsesDefault(t *testing.T) {
	f := NewToolRejectFormatter(nil)
	if f == nil || f.dedup == nil {
		t.Fatal("expected default deduper")
	}
}

func phaseFixtureCopy() map[string]string {
	return map[string]string{"what": "observed refusal", "cause": "structured cause", "why": "preserve the boundary", "fix": "recover {{ literal.path }}", "instead": "use {% literal.branch %}"}
}

func TestPhaseFeedbackPreservesEveryCopyField(t *testing.T) {
	setTestRenderer(t)
	copy := phaseFixtureCopy()
	out := denyOutcome{rejectCode: "FIXTURE", phase: "2", min: "branch-specific action"}
	block, err := NewToolRejectFormatter(nil).FormatBlock(t.Context(), "s1", "task", out, PlanProgress{}, copy)
	testutil.FailErr(t, "render phase feedback", err)
	for field, value := range copy {
		if !strings.Contains(block, value) {
			t.Fatalf("lost frozen %s: %s", field, block)
		}
	}
	if !strings.Contains(block, out.min) {
		t.Fatalf("lost host branch requirement: %s", block)
	}
}
