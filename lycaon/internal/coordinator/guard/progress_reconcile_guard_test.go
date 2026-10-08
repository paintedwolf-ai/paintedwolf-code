package guard

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestObserveProgressReconcileOnSynthesis_allowsClose(t *testing.T) {
	fmt := wrapupGuardFormatter(t)
	current := "## Progress\n- [ ] survey"
	proposed := "## Progress\n- [x] survey"
	_, block := formatObservationReject(t, fmt, func(gc *oar.GuardContext) {
		gc.ObserveToolCall("update_progress", nil)
		ObserveProgressReconcileOnSynthesis(
			spawn.SurfaceImplementSynthesis,
			current,
			map[string]any{"content": proposed},
			gc,
		)
	})
	if block {
		t.Fatal("reconcile close must not observe")
	}
}

func TestObserveProgressReconcileOnSynthesis_rejectsNewScope(t *testing.T) {
	fmt := wrapupGuardFormatter(t)
	current := "## Progress\n- [x] survey"
	proposed := "## Progress\n- [x] survey\n- [ ] more"
	text, block := formatObservationReject(t, fmt, func(gc *oar.GuardContext) {
		gc.ObserveToolCall("update_progress", nil)
		ObserveProgressReconcileOnSynthesis(
			spawn.SurfaceImplementSynthesis,
			current,
			map[string]any{"content": proposed},
			gc,
		)
	})
	if !block || !strings.Contains(text, ProgressSynthesisReconcileOnlyCode) {
		t.Fatalf("block=%v text=%q", block, text)
	}
}

func TestObserveProgressReconcileOnSynthesis_skipsInvestigate(t *testing.T) {
	fmt := wrapupGuardFormatter(t)
	_, block := formatObservationReject(t, fmt, func(gc *oar.GuardContext) {
		gc.ObserveToolCall("update_progress", nil)
		ObserveProgressReconcileOnSynthesis(
			tools.SurfaceImplementInvestigate,
			"",
			map[string]any{"content": "## Progress\n- [ ] new"},
			gc,
		)
	})
	if block {
		t.Fatal("investigate must not use synthesis reconcile guard")
	}
}

func TestObserveProgressReconcileIgnoresOtherContentTools(t *testing.T) {
	gc := oar.NewGuardContext()
	gc.ObserveToolCall("write", nil)
	ObserveProgressReconcileOnSynthesis(spawn.SurfaceImplementSynthesis, "- [x] done", map[string]any{"content": "- [ ] unrelated"}, gc)
	if gc.Progress.ProgressReconcileNeeded {
		t.Fatal("unrelated tool became a checklist update")
	}
}
