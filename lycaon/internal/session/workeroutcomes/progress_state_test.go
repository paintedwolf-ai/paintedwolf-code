package workeroutcomes_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/session/progressclosure"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workerrecords"
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

type staticJobNames map[string]workerrecords.JobNames

func (s staticJobNames) NamesByJob(_ context.Context, jobID string) (workerrecords.JobNames, bool, error) {
	names, ok := s[jobID]
	return names, ok, nil
}

func TestCurrentProgressSnapshotUsesRootStoreIndependentlyOfHistory(t *testing.T) {
	store := progresstest.New(t, "root", "child")
	progresstest.SeedRows(t, store, "root",
		progress.RowSpec{Label: "Deliver"},
		progress.RowSpec{Label: "Verify"},
	)
	progresstest.SeedRows(t, store, "child", progress.RowSpec{Label: "Wrong scope"})

	closure := progressclosure.New()
	closure.SetProgress(store)
	closure.SetNames(staticJobNames{"worker-one": {Worker: "scout", Handle: "scout#1"}})
	closure.Arm(t.Context(), "root", "worker-one")

	sessions := &progressSessions{}
	state := workeroutcomes.NewState(workeroutcomes.StatePorts{SessionStore: sessions, TranscriptStore: sessions})
	state.SetProgress(store)
	state.SetProgressReconciliation(closure)

	var previousLabel string
	for _, historyErr := range []error{nil, errors.New("transcript unavailable")} {
		sessions.historyErr = historyErr
		current := state.ForSession(t.Context(), &api.Session{ID: "child", ParentSessionID: "root"})
		var snapshot progress.RequestSnapshot
		testutil.FailErr(t, "decode authoritative snapshot", json.Unmarshal([]byte(current.ProgressSnapshot), &snapshot))
		expected, err := store.Snapshot(t.Context(), "root")
		testutil.FailErr(t, "read stored snapshot", err)
		if snapshot.ScopeSessionID != "root" || snapshot.InitializationRequired || !slices.Equal(snapshot.Rows, expected.Rows) || current.ProgressOpenCount != 2 || current.ProgressMissing {
			t.Fatalf("stored facts were lost: %+v / %+v", snapshot, current)
		}
		if len(snapshot.UnaccountedResults) != 1 || snapshot.UnaccountedResults[0].Handle != "scout#1" || strings.Contains(current.ProgressSnapshot, "worker-one") {
			t.Fatalf("wrong root finish set: %+v", snapshot)
		}
		if previousLabel != "" && previousLabel == snapshot.Rows[0].Label {
			t.Fatal("current snapshot reused older transcript state")
		}
		previousLabel = snapshot.Rows[0].Label
		progresstest.UpdateRows(t, store, "root", progress.UpdateRowSpec{
			ID: "p1", Label: progresstest.Label("Deliver revised"),
		})
	}
	if state.ForSession(t.Context(), nil).ProgressSnapshot != "" {
		t.Fatal("nil session exposed progress state")
	}
}

func TestEmptyProgressExplicitlyRequiresInitialization(t *testing.T) {
	state := workeroutcomes.NewState(workeroutcomes.StatePorts{
		SessionStore:    &progressSessions{},
		TranscriptStore: &progressSessions{},
		Progress:        progresstest.New(t, "root", "child"),
	})
	current := state.ForSession(t.Context(), &api.Session{ID: "child", ParentSessionID: "root"})
	var snapshot progress.RequestSnapshot
	testutil.FailErr(t, "decode empty checklist", json.Unmarshal([]byte(current.ProgressSnapshot), &snapshot))
	if snapshot.ScopeSessionID != "root" || !snapshot.InitializationRequired || !current.ProgressMissing || len(snapshot.Rows) != 0 {
		t.Fatalf("empty current checklist was ambiguous: %+v / %+v", snapshot, current)
	}
}

func TestSessionStateCountsDeliveredWorkers(t *testing.T) {
	for _, tc := range []struct {
		n    int
		err  error
		want int
	}{{2, nil, 2}, {5, errors.New("store closed"), 0}} {
		state := workeroutcomes.NewState(workeroutcomes.StatePorts{SessionStore: &progressSessions{}, TranscriptStore: &progressSessions{}})
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
