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

type modeStore struct {
	SessionRepository
	SubmissionRepository
	TurnRepository
}

func runModeStoreCases(t *testing.T, check func(*testing.T, modeStore)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		db := testdbfixture.Open(t, "mode.db")
		testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, t.TempDir())
		repos := NewSQL(db)
		check(t, modeStore{
			SessionRepository:    repos.Sessions,
			SubmissionRepository: repos.Submissions,
			TurnRepository:       repos.Turns,
		})
	})
}

func TestSessionModeResolutionFailsClosedWithoutStore(t *testing.T) {
	for _, st := range []interface {
		ResolveEffectiveMode(context.Context, string) (RootAuthority, error)
	}{(*sqlSessions)(nil), (*Repositories)(nil)} {
		if _, err := st.ResolveEffectiveMode(t.Context(), "root"); !errors.Is(err, ErrSessionModeUnavailable) {
			t.Fatalf("nil %T authority lookup = %v", st, err)
		}
	}
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
		_, _, err = st.PutPromptSubmission(ctx, input)
		testutil.FailErr(t, "admit Ask prompt", err)
		assertSessionMode(t, st, root.ID, root.ID, ask, true)
		assertSessionMode(t, st, grandchild.ID, root.ID, ask, true)
		if _, err := st.SetMode(ctx, child.ID, api.SessionModeAgent); !errors.Is(err, ErrChildSessionMode) {
			t.Fatalf("worker mode update = %v", err)
		}
		_, err = st.SetMode(ctx, root.ID, api.SessionModeAgent)
		testutil.FailErr(t, "upgrade root to Agent", err)
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

func TestSessionModeRejectsMalformedInputAndMissingSessions(t *testing.T) {
	runModeStoreCases(t, func(t *testing.T, st modeStore) {
		ctx := t.Context()
		invalid := api.SessionMode("")
		if _, err := st.Create(ctx, api.CreateSessionRequest{Mode: &invalid}, testdbseed.DefaultProjectID); !errors.Is(err, ErrInvalidSessionMode) {
			t.Fatalf("create with malformed mode = %v", err)
		}
		if _, err := st.SetMode(ctx, "missing", api.SessionModeAsk); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("set missing session mode = %v", err)
		}
		root, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
		testutil.FailErr(t, "create new session", err)
		if _, err := st.SetMode(ctx, root.ID, invalid); !errors.Is(err, ErrInvalidSessionMode) {
			t.Fatalf("patch with malformed mode = %v", err)
		}
		if _, _, err := st.PutPromptSubmission(ctx, modePrompt(root, &invalid)); !errors.Is(err, ErrInvalidSessionMode) {
			t.Fatalf("prompt with malformed mode = %v", err)
		}
		assertSessionMode(t, st, root.ID, root.ID, api.SessionModeAgent, false)
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

func TestSessionModeWorkflowAdmissionRollsBackWithCaller(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "mode.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	repos := NewSQL(database)
	st := modeStore{
		SessionRepository:    repos.Sessions,
		SubmissionRepository: repos.Submissions,
		TurnRepository:       repos.Turns,
	}
	root, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create unstarted chat", err)
	intent := api.SessionModeAsk
	failedRun := errors.New("simulate failure inserting a workflow run")
	err = st.WithModeAdmission(ctx, root.ID, func(ctx context.Context) error {
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		err = st.AdmitModeInTx(ctx, tx, root.ID, &intent, func(mode api.SessionMode) error {
			if mode != intent {
				return errors.New("workflow validation received the wrong mode")
			}
			return nil
		})
		if err != nil {
			return err
		}
		return failedRun
	})
	if !errors.Is(err, failedRun) {
		t.Fatalf("workflow admission error = %v", err)
	}
	assertSessionMode(t, st, root.ID, root.ID, api.SessionModeAgent, false)
}

func TestSessionModeAskToAgentUpgradePersistsAcrossSQLReopenAndIgnoresPreStartSwitches(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	dbPath := dir + "/mode-upgrade.db"
	sqlDB := testdbfixture.OpenPath(t, dbPath)
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	st := NewSQL(sqlDB)

	ask := api.SessionModeAsk
	// 1. An unstarted Ask chat switched to Agent before any turn begins is an ordinary Agent chat, not an upgrade.
	ordinary, err := st.Sessions.Create(ctx, api.CreateSessionRequest{Mode: &ask}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create unstarted Ask session", err)
	_, err = st.Sessions.SetMode(ctx, ordinary.ID, api.SessionModeAgent)
	testutil.FailErr(t, "switch unstarted Ask session to Agent", err)
	_, _, err = st.Submissions.PutPromptSubmission(ctx, modePrompt(ordinary, nil))
	testutil.FailErr(t, "start ordinary Agent session", err)
	ordAuth, err := st.Sessions.ResolveEffectiveMode(ctx, ordinary.ID)
	testutil.FailErr(t, "read ordinary Agent authority", err)
	if ordAuth.UpgradedFromAsk {
		t.Fatalf("unstarted Ask->Agent switch marked UpgradedFromAsk: %+v", ordAuth)
	}

	// 2. A started Ask chat switched to Agent records durable UpgradedFromAsk evidence that survives DB reopen.
	startedAsk, err := st.Sessions.Create(ctx, api.CreateSessionRequest{Mode: &ask}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create started Ask session", err)
	_, _, err = st.Submissions.PutPromptSubmission(ctx, modePrompt(startedAsk, &ask))
	testutil.FailErr(t, "admit first Ask prompt", err)
	_, err = st.Sessions.SetMode(ctx, startedAsk.ID, api.SessionModeAgent)
	testutil.FailErr(t, "upgrade started Ask session to Agent", err)
	upAuth, err := st.Sessions.ResolveEffectiveMode(ctx, startedAsk.ID)
	testutil.FailErr(t, "read upgraded authority before reopen", err)
	if !upAuth.UpgradedFromAsk || upAuth.AgentNoticeDelivered || upAuth.Mode != api.SessionModeAgent || !upAuth.Started {
		t.Fatalf("upgraded authority before reopen = %+v", upAuth)
	}
	testutil.FailErr(t, "close SQL DB for reopen", sqlDB.Close())

	reopenedDB := testdbfixture.OpenPath(t, dbPath)
	reopenedStore := NewSQL(reopenedDB)
	reopenedAuth, err := reopenedStore.Sessions.ResolveEffectiveMode(ctx, startedAsk.ID)
	testutil.FailErr(t, "read upgraded authority after reopen", err)
	if !reopenedAuth.UpgradedFromAsk || reopenedAuth.AgentNoticeDelivered || reopenedAuth.Mode != api.SessionModeAgent || !reopenedAuth.Started {
		t.Fatalf("upgraded authority after reopen = %+v", reopenedAuth)
	}
	testutil.FailErr(t, "append agent_enabled notice", reopenedStore.Transcript.AppendMessages(ctx, startedAsk.ID, api.Message{
		ID:           AgentEnabledMessageID(startedAsk.ID),
		Role:         api.MessageRoleUser,
		Origin:       api.MessageOriginHost,
		Kind:         api.MessageKindHostNudge,
		HostSignalID: agentModeEnabledSignalID,
		Content:      "[host:session-agent-enabled]\nAgent mode is now active.",
	}))
	deliveredAuth, err := reopenedStore.Sessions.ResolveEffectiveMode(ctx, startedAsk.ID)
	testutil.FailErr(t, "read authority after notice delivery", err)
	if !deliveredAuth.UpgradedFromAsk || !deliveredAuth.AgentNoticeDelivered {
		t.Fatalf("authority after notice delivery = %+v", deliveredAuth)
	}
	if _, err := reopenedDB.ExecContext(ctx, "UPDATE sessions SET upgraded_from_ask = 0 WHERE id = ?", startedAsk.ID); err == nil {
		t.Fatal("SQLite accepted resetting upgraded_from_ask on a started session")
	}
	reopenedOrd, err := reopenedStore.Sessions.ResolveEffectiveMode(ctx, ordinary.ID)
	testutil.FailErr(t, "read ordinary authority after reopen", err)
	if reopenedOrd.UpgradedFromAsk || reopenedOrd.AgentNoticeDelivered {
		t.Fatalf("ordinary Agent session marked UpgradedFromAsk after reopen: %+v", reopenedOrd)
	}
}

func TestCorruptSessionModeDoesNotPreventDiagnosisOrDelete(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "mode.db")
	testdbseed.InsertSession(t, database, "coord", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "unrelated", testdbseed.DefaultProjectID)
	testdbseed.InsertChildSession(t, database, "worker", "coord")
	st := NewSQL(database).Sessions

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
	if authority.RootSessionID != rootID || authority.Mode != mode || authority.Started != started {
		t.Fatalf("mode authority = %+v, want root=%s mode=%s started=%t", authority, rootID, mode, started)
	}
	projected, err := st.Get(t.Context(), id)
	testutil.FailErr(t, "read session view", err)
	if projected.RootSessionID != rootID || projected.Mode != mode || projected.Started != started {
		t.Fatalf("session projection = %+v, want root=%s mode=%s started=%t", projected, rootID, mode, started)
	}
}
