package guard

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestObserveProgressReconcileOnSynthesis_allowsClose(t *testing.T) {
	fmt := wrapupGuardFormatter(t)
	current := progress.Snapshot{Rows: []progress.Row{{ID: "p1", State: progress.ProgressStatePending, Label: "survey"}}}
	_, block := formatObservationReject(t, fmt, func(gc *oar.GuardContext) {
		gc.ObserveToolCall("update_progress", nil)
		ObserveProgressReconcileOnSynthesis(
			spawn.SurfaceImplementSynthesis,
			current,
			map[string]any{"update": []any{map[string]any{"id": "p1", "state": "done"}}},
			gc,
		)
	})
	if block {
		t.Fatal("reconcile close must not observe")
	}
}

func TestObserveProgressReconcileOnSynthesis_rejectsNewScope(t *testing.T) {
	fmt := wrapupGuardFormatter(t)
	current := progress.Snapshot{Rows: []progress.Row{{ID: "p1", State: progress.ProgressStateDone, Label: "survey"}}}
	text, block := formatObservationReject(t, fmt, func(gc *oar.GuardContext) {
		gc.ObserveToolCall("update_progress", nil)
		ObserveProgressReconcileOnSynthesis(
			spawn.SurfaceImplementSynthesis,
			current,
			map[string]any{"add": []any{map[string]any{"label": "more"}}},
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
			toolcontract.SurfaceImplementInvestigate,
			progress.Snapshot{},
			map[string]any{"replace": []any{map[string]any{"label": "new"}}},
			gc,
		)
	})
	if block {
		t.Fatal("investigate must not use synthesis reconcile guard")
	}
}

func TestObserveProgressReconcileIgnoresOtherTools(t *testing.T) {
	gc := oar.NewGuardContext()
	gc.ObserveToolCall("write", nil)
	ObserveProgressReconcileOnSynthesis(
		spawn.SurfaceImplementSynthesis,
		progress.Snapshot{Rows: []progress.Row{{ID: "p1", State: progress.ProgressStateDone, Label: "done"}}},
		map[string]any{"add": []any{map[string]any{"label": "unrelated"}}},
		gc,
	)
	if gc.Progress.ProgressReconcileNeeded {
		t.Fatal("unrelated tool became a checklist update")
	}
}
