package workeroutcomes_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestCurrentProgressSnapshotUsesRootStoreIndependentlyOfHistory(t *testing.T) {
	store := progress.NewMemoryStore()
	testutil.FailErr(t, "seed root checklist", store.Set("root", "- [ ] Deliver\n- [ ] Verify"))
	testutil.FailErr(t, "seed unrelated child state", store.Set("child", "- [ ] Wrong scope"))
	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.Arm(t.Context(), "root", "worker-one")
	sessions := &progressSessions{}
	state := workeroutcomes.NewState(workeroutcomes.StatePorts{Sessions: sessions})
	state.SetProgress(store)
	state.SetProgressReconciliation(closure)
	var previousRevision string
	for _, historyErr := range []error{nil, errors.New("transcript unavailable")} {
		sessions.historyErr = historyErr
		current := state.ForSession(t.Context(), &api.Session{ID: "child", ParentSessionID: "root"})
		var snapshot progress.RequestSnapshot
		testutil.FailErr(t, "decode authoritative snapshot", json.Unmarshal([]byte(current.ProgressSnapshot), &snapshot))
		expected, err := store.Snapshot(t.Context(), "root")
		testutil.FailErr(t, "read stored snapshot", err)
		if snapshot.ScopeSessionID != "root" || snapshot.InitializationRequired || snapshot.Content != expected.Content || snapshot.Revision != expected.Revision || current.ProgressOpenCount != 2 || current.ProgressMissing {
			t.Fatalf("stored facts were lost: %+v / %+v", snapshot, current)
		}
		if snapshot.Reconciliation == nil || snapshot.Reconciliation.Token != closure.Reconciliation("root").Token || strings.Contains(current.ProgressSnapshot, "worker-one") {
			t.Fatalf("wrong root finish set: %+v", snapshot)
		}
		if previousRevision != "" && previousRevision == snapshot.Revision {
			t.Fatal("current snapshot reused older transcript state")
		}
		previousRevision = snapshot.Revision
		testutil.FailErr(t, "publish current label change", store.Set("root", "- [ ] Deliver revised\n- [ ] Verify"))
	}
	if state.ForSession(t.Context(), nil).ProgressSnapshot != "" {
		t.Fatal("nil session exposed progress state")
	}
}

func TestEmptyProgressExplicitlyRequiresInitialization(t *testing.T) {
	state := workeroutcomes.NewState(workeroutcomes.StatePorts{Sessions: &progressSessions{}, Progress: progress.NewMemoryStore()})
	current := state.ForSession(t.Context(), &api.Session{ID: "child", ParentSessionID: "root"})
	var snapshot progress.RequestSnapshot
	testutil.FailErr(t, "decode empty checklist", json.Unmarshal([]byte(current.ProgressSnapshot), &snapshot))
	if snapshot.ScopeSessionID != "root" || !snapshot.InitializationRequired || !current.ProgressMissing || snapshot.Revision == "" {
		t.Fatalf("empty current checklist was ambiguous: %+v / %+v", snapshot, current)
	}
}

// Surface selection reads the delivered worker count; a failed count leaves it
// at zero.
func TestSessionStateCountsDeliveredWorkers(t *testing.T) {
	for _, tc := range []struct {
		n    int
		err  error
		want int
	}{{2, nil, 2}, {5, errors.New("store closed"), 0}} {
		state := workeroutcomes.NewState(workeroutcomes.StatePorts{Sessions: &progressSessions{}})
		state.SetDeliveredWorkers(func(_ context.Context, sessionID string) (int, error) {
			if sessionID != "coord" {
				t.Fatalf("counted in session %q", sessionID)
			}
			return tc.n, tc.err
		})
		if got := state.ForSession(t.Context(), &api.Session{ID: "coord"}).WorkersDelivered; got != tc.want {
			t.Fatalf("delivered = %d want %d", got, tc.want)
		}
	}
}
