package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type modeStore interface {
	Create(context.Context, api.CreateSessionRequest, string) (*api.Session, error)
	CreateChild(context.Context, *api.Session, api.SpawnChildRequest) (*api.Session, error)
	Get(context.Context, string) (*api.Session, error)
	UpdateSession(context.Context, string, func(*api.Session)) error
	SetMode(context.Context, string, api.SessionMode) (*api.Session, error)
	ResolveEffectiveMode(context.Context, string) (RootAuthority, error)
	PutPromptSubmission(context.Context, PromptSubmission) (*PromptSubmission, bool, error)
	GetPromptSubmission(context.Context, string) (*PromptSubmission, error)
	BeginTurn(context.Context, TurnStart) (TurnExecution, error)
}

func runModeStoreCases(t *testing.T, check func(*testing.T, modeStore)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { check(t, NewMemory()) })
	t.Run("sqlite", func(t *testing.T) {
		db := testdbfixture.Open(t, "mode.db")
		testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, t.TempDir())
		check(t, NewSQL(db))
	})
}

func TestSessionModeInheritedAndLatchedByAdmission(t *testing.T) {
	runModeStoreCases(t, func(t *testing.T, st modeStore) {
		ctx := t.Context()
		root, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create root session", err)
		child, err := st.CreateChild(ctx, root, api.SpawnChildRequest{AgentType: "repo-researcher"})
		testutil.FailErr(t, "create worker session", err)
		grandchild, err := st.CreateChild(ctx, child, api.SpawnChildRequest{AgentType: "repo-researcher"})
		testutil.FailErr(t, "create nested worker session", err)
		assertSessionMode(t, st, grandchild.ID, root.ID, api.SessionModeAgent, false)

		ask := api.SessionModeAsk
		input := modePrompt(root, &ask)
		if _, _, err := st.PutPromptSubmission(ctx, input); err != nil {
			testutil.FailErr(t, "admit Ask prompt", err)
		}
		assertSessionMode(t, st, root.ID, root.ID, ask, true)
		assertSessionMode(t, st, grandchild.ID, root.ID, ask, true)
		if _, err := st.SetMode(ctx, child.ID, api.SessionModeAgent); !errors.Is(err, ErrChildSessionMode) {
			t.Fatalf("worker mode update = %v", err)
		}
		if _, err := st.SetMode(ctx, root.ID, api.SessionModeAgent); err != nil {
			testutil.FailErr(t, "upgrade root to Agent", err)
		}
		if _, inserted, err := st.PutPromptSubmission(ctx, input); inserted || err != nil {
			t.Fatalf("retry Ask prompt after upgrade: inserted = %v, err = %v", inserted, err)
		}
		assertSessionMode(t, st, grandchild.ID, root.ID, api.SessionModeAgent, true)

		if _, err := st.SetMode(ctx, root.ID, ask); !errors.Is(err, ErrSessionModeLocked) {
			t.Fatalf("mode regression = %v", err)
		}
		rejected := modePrompt(root, &ask)
		if _, _, err := st.PutPromptSubmission(ctx, rejected); !errors.Is(err, ErrSessionModeLocked) {
			t.Fatalf("prompt mode regression = %v", err)
		}
		if _, err := st.GetPromptSubmission(ctx, rejected.ID); !errors.Is(err, ErrPromptSubmissionNotFound) {
			t.Fatalf("rejected prompt left a durable receipt: %v", err)
		}
		testutil.FailErr(t, "rename root chat", st.UpdateSession(ctx, root.ID, func(sess *api.Session) {
			sess.Title = "Retained title"
			sess.Mode, sess.Started = ask, false
		}))
		assertSessionMode(t, st, root.ID, root.ID, api.SessionModeAgent, true)
	})
}

func TestSessionModeFirstPromptRacesPreStartPatch(t *testing.T) {
	runModeStoreCases(t, func(t *testing.T, st modeStore) {
		ctx := t.Context()
		root, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create root", err)
		start := make(chan struct{})
		promptDone, patchDone := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, _, err := st.PutPromptSubmission(ctx, modePrompt(root, nil))
			promptDone <- err
		}()
		go func() {
			<-start
			_, err := st.SetMode(ctx, root.ID, api.SessionModeAsk)
			patchDone <- err
		}()
		close(start)
		testutil.FailErr(t, "admit concurrent prompt", <-promptDone)
		switch err := <-patchDone; {
		case err == nil:
			assertSessionMode(t, st, root.ID, root.ID, api.SessionModeAsk, true)
		case errors.Is(err, ErrSessionModeLocked):
			assertSessionMode(t, st, root.ID, root.ID, api.SessionModeAgent, true)
		default:
			testutil.FailErr(t, "patch concurrent mode", err)
		}
	})
}

func TestSessionModeDirectTurnLatchesAuthority(t *testing.T) {
	runModeStoreCases(t, func(t *testing.T, st modeStore) {
		mode := api.SessionModeAsk
		root, err := st.Create(t.Context(), api.CreateSessionRequest{Mode: &mode}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create Ask session", err)
		_, err = st.BeginTurn(t.Context(), TurnStart{SessionID: root.ID, ProjectID: root.ProjectID, Origin: TurnOriginUser, InputJSON: "{}"})
		testutil.FailErr(t, "begin direct turn", err)
		assertSessionMode(t, st, root.ID, root.ID, mode, true)
	})
}

func TestCorruptSessionModeDoesNotPreventDiagnosisOrDelete(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "mode.db")
	testdbseed.InsertSession(t, database, "coord", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "unrelated", testdbseed.DefaultProjectID)
	testdbseed.InsertChildSession(t, database, "worker", "coord")
	st := NewSQL(database)

	if _, err := database.ExecContext(ctx, "UPDATE sessions SET root_session_id = 'unrelated' WHERE id = 'worker'"); err == nil {
		t.Fatal("SQLite accepted a root lineage update")
	}
	_, err := database.ExecContext(ctx, "DROP TRIGGER sessions_lineage_immutable")
	testutil.FailErr(t, "disable lineage trigger", err)
	_, err = database.ExecContext(ctx, "UPDATE sessions SET root_session_id = 'unrelated' WHERE id = 'worker'")
	testutil.FailErr(t, "seed corrupted lineage", err)
	if _, err := st.ResolveEffectiveMode(ctx, "worker"); !errors.Is(err, ErrSessionModeUnavailable) {
		t.Fatalf("corrupted mode lookup = %v", err)
	}
	view, err := st.Get(ctx, "worker")
	testutil.FailErr(t, "diagnose corrupted worker session", err)
	if view.Mode != "" || view.Started {
		t.Fatalf("diagnostic view projected unverified authority: %q, started = %v", view.Mode, view.Started)
	}
	testutil.FailErr(t, "delete corrupted worker session", st.Delete(ctx, "worker"))
}

func modePrompt(root *api.Session, mode *api.SessionMode) PromptSubmission {
	return PromptSubmission{
		ID: uuid.NewString(), SessionID: root.ID, ProjectID: root.ProjectID,
		InputDigest: "mode test", InputJSON: "{}", Mode: mode,
		Origin: PromptSubmissionOriginUser, SubmittedBy: root.OwnerPersonID,
	}
}

func assertSessionMode(t *testing.T, st modeStore, id, rootID string, mode api.SessionMode, started bool) {
	t.Helper()
	authority, err := st.ResolveEffectiveMode(t.Context(), id)
	testutil.FailErr(t, "read mode authority", err)
	want := RootAuthority{RootSessionID: rootID, Mode: mode, Started: started}
	if authority != want {
		t.Fatalf("mode authority = %+v, want %+v", authority, want)
	}
	projected, err := st.Get(t.Context(), id)
	testutil.FailErr(t, "read session view", err)
	if projected.RootSessionID != rootID || projected.Mode != mode || projected.Started != started {
		t.Fatalf("session projection = %+v, want mode %+v", projected, want)
	}
}
