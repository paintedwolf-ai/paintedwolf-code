package workerbriefing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type summaryGenerator struct{}

func (summaryGenerator) Generate(context.Context, briefing.Invocation) (string, string, string, error) {
	return "Contribution: fixed.\nVerification: built.\nRemaining: none.", "provider", "model", nil
}

type snapshots chan briefing.Snapshot

func (s snapshots) PublishSnapshot(_ context.Context, _ string, snap briefing.Snapshot) { s <- snap }

// The schema decides which targets can be stored, so the resolved request is
// started against the real store.
func TestSettledRoundTargetIsStored(t *testing.T) {
	const project = testdbseed.DefaultProjectID
	database := testdbfixture.Open(t, "worker-briefing.db")
	testdbseed.InsertProjectRoot(t, database, project, t.TempDir())
	store := briefingstore.NewStore(database, t.TempDir())
	scheduler := briefing.NewScheduler(context.Background())
	t.Cleanup(func() { scheduler.Stop(); scheduler.Wait(context.Background()) })
	published := make(snapshots, 8)
	engine := briefing.NewEngine(scheduler, store, summaryGenerator{}, published, nil)

	task := job()
	task.ProjectID = project
	raw, err := json.Marshal(wire.WorkerResultBriefingTarget{ResultHandle: resultHandle1})
	testutil.FailErr(t, "target", err)
	request, err := New(workers{resultHandle1: task}, finals{}).Resolve(t.Context(), briefing.Target{ProjectID: project, Ref: raw})
	testutil.FailErr(t, "resolve", err)
	pending, err := engine.Request(t.Context(), request)
	testutil.FailErr(t, "request", err)

	stored, err := store.GetTarget(t.Context(), project, ProfileID, resultHandle1)
	testutil.FailErr(t, "stored target", err)
	var ref wire.WorkerResultBriefingTarget
	testutil.FailErr(t, "stored target json", json.Unmarshal([]byte(stored.TargetJSON), &ref))
	if stored.TargetKey != resultHandle1 || ref.ResultHandle != resultHandle1 || pending.AttemptID == "" {
		t.Fatalf("stored %+v after %+v", stored, pending)
	}
}
