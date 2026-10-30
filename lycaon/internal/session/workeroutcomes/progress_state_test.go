package workeroutcomes_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type progressSessions struct{ historyErr error }

func (p *progressSessions) Get(_ context.Context, id string) (*api.Session, error) {
	parent := ""
	if id == "child" {
		parent = "root"
	}
	return &api.Session{ID: id, ParentSessionID: parent}, nil
}
func (p *progressSessions) GetMessages(context.Context, string) ([]api.Message, error) {
	return nil, p.historyErr
}

func TestCurrentProgressSnapshotUsesRootOwnerIndependentlyOfHistory(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed root checklist", store.Set("root", "- [ ] Deliver\n- [ ] Verify"))
	testutil.FailErr(t, "seed unrelated child state", store.Set("child", "- [ ] Wrong scope"))
	owner := progressclosure.New()
	owner.SetProgress(store)
	owner.Arm(t.Context(), "root", "worker-one")
	sessions := &progressSessions{}
	state := workeroutcomes.NewState(workeroutcomes.StatePorts{Sessions: sessions})
	state.SetProgress(store)
	state.SetProgressReconciliation(owner)
	var previousRevision string
	for _, historyErr := range []error{nil, errors.New("transcript unavailable")} {
		sessions.historyErr = historyErr
		current := state.ForSession(t.Context(), &api.Session{ID: "child", ParentSessionID: "root"})
		var snapshot progress.Snapshot
		testutil.FailErr(t, "decode authoritative snapshot", json.Unmarshal([]byte(current.ProgressSnapshot), &snapshot))
		expected, err := store.Snapshot(t.Context(), "root")
		testutil.FailErr(t, "read owner snapshot", err)
		if snapshot.Content != expected.Content || snapshot.Revision != expected.Revision || current.ProgressOpenCount != 2 || current.ProgressMissing {
			t.Fatalf("owner facts were lost: %+v / %+v", snapshot, current)
		}
		if snapshot.Reconciliation == nil || snapshot.Reconciliation.Token != owner.Reconciliation("root").Token || len(snapshot.Reconciliation.Jobs) != 1 || snapshot.Reconciliation.Jobs[0] != "worker-one" {
			t.Fatalf("wrong root finish set: %+v", snapshot)
		}
		if previousRevision != "" && previousRevision == snapshot.Revision {
			t.Fatal("current snapshot reused older transcript state")
		}
		previousRevision = snapshot.Revision
		testutil.FailErr(t, "publish current label change", store.Set("root", "- [ ] Deliver revised\n- [ ] Verify"))
	}
	if state.ForSession(t.Context(), nil).ProgressSnapshot != "" {
		t.Fatal("nil session exposed owner state")
	}
}
