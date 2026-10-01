package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func detectionGate(t *testing.T, sources settings.Sources) hitl.ApprovalGate {
	t.Helper()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	return settings.NewRuleApprovalGate(store, sources)
}

func containedAction(sessionID string) hitl.ProposedAction {
	return hitl.ProposedAction{
		Tool:      "command",
		Args:      map[string]any{"command": "echo hi"},
		Contained: hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy},
		SessionID: sessionID,
	}
}

// Missing detection sources produce incomplete facts.
func TestUnwiredDetectionSourceRaisesIncompleteFacts(t *testing.T) {
	t.Parallel()
	g := detectionGate(t, settings.Sources{})
	res, err := g.Evaluate(context.Background(), containedAction("sess-unwired"))
	testutil.FailErr(t, "Evaluate", err)
	if res == nil || !res.Required() {
		t.Fatalf("an unwired detection engine must ask, got %+v", res)
	}
	if got := res.Decision.Primary; got != api.GateIncompleteFacts {
		t.Fatalf("gate = %q, want %q", got, api.GateIncompleteFacts)
	}
}

func TestGateBuilderSealsWithoutDetectionAsIncompleteFacts(t *testing.T) {
	t.Parallel()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	builder, handle := settings.NewGateBuilder(store)
	builder.Seal()
	res, err := handle.Evaluate(context.Background(), containedAction("sess-sealed-unwired"))
	testutil.FailErr(t, "Evaluate", err)
	if res == nil || !res.Required() || res.Decision.Primary != api.GateIncompleteFacts {
		t.Fatalf("a gate sealed with no detection source must ask incomplete facts, got %+v", res)
	}
}

// An empty catalog reports a completed evaluation with no matches.
func TestWiredDetectionSourceWithNoMatchStaysSilent(t *testing.T) {
	t.Parallel()
	g := detectionGate(t, settings.NoSources())
	res, err := g.Evaluate(context.Background(), containedAction("sess-wired-inert"))
	testutil.FailErr(t, "Evaluate", err)
	if res != nil && res.Required() {
		t.Fatalf("a wired engine that matched nothing must stay silent, got %+v", res)
	}
}

func TestDetectionSourceSnapshotsOncePerAction(t *testing.T) {
	t.Parallel()
	sources := settings.NoSources()
	source := sources.Detections()
	reads := 0
	sources.Detections = func() settings.DetectionSource {
		reads++
		return source
	}
	g := detectionGate(t, sources)
	for attempt := range 3 {
		_, err := g.Evaluate(t.Context(), containedAction("snapshot-session"))
		testutil.FailErr(t, "evaluate detection snapshot", err)
		if reads != attempt+1 {
			t.Fatalf("source reads = %d after %d actions", reads, attempt+1)
		}
	}
}
