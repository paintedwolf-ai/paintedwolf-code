package submissions

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/promptstate"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissionstate"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAmbientWakeEligibleChecksGatesAndQueues(t *testing.T) {
	var nilSvc *Service
	if ok, retry := nilSvc.AmbientWakeEligible(t.Context(), "s1"); ok || retry {
		t.Fatalf("nil AmbientWakeEligible = (%v, %v), want (false, false)", ok, retry)
	}

	db := testdbfixture.Open(t, "ambient_wake.db")
	testdbseed.InsertProjectRoot(t, db, testdbseed.DefaultProjectID, t.TempDir())
	st := store.NewSQL(db)
	sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	testdbseed.InsertChildSession(t, db, "child-1", sess.ID)

	q := queue.New()
	gate := lifecycle.New(st.Sessions)
	blocked := false
	svc := New(
		st.Sessions,
		st.Submissions,
		st.People,
		q,
		gate,
		nil,
		nil,
		nil,
		&submissionstate.Service{},
		&promptstate.MutexRegistry{},
		nil,
		nil,
		nil,
		func(context.Context, string) bool { return blocked },
	)

	if ok, retry := svc.AmbientWakeEligible(t.Context(), "missing"); ok || retry {
		t.Fatalf("missing session = (%v, %v), want (false, false)", ok, retry)
	}
	if ok, retry := svc.AmbientWakeEligible(t.Context(), "child-1"); ok || retry {
		t.Fatalf("worker child = (%v, %v), want (false, false)", ok, retry)
	}

	testutil.FailErr(t, "mark busy", st.Sessions.SetSessionStatus(t.Context(), sess.ID, api.SessionStatusBusy))
	if ok, retry := svc.AmbientWakeEligible(t.Context(), sess.ID); ok || !retry {
		t.Fatalf("busy session = (%v, %v), want (false, true)", ok, retry)
	}
	testutil.FailErr(t, "mark idle", st.Sessions.SetSessionStatus(t.Context(), sess.ID, api.SessionStatusIdle))

	blocked = true
	if ok, retry := svc.AmbientWakeEligible(t.Context(), sess.ID); ok || retry {
		t.Fatalf("host-blocked session = (%v, %v), want (false, false)", ok, retry)
	}
	blocked = false

	flight, _ := gate.Begin(sess.ID)
	if ok, retry := svc.AmbientWakeEligible(t.Context(), sess.ID); ok || retry {
		t.Fatalf("stopping session = (%v, %v), want (false, false)", ok, retry)
	}
	gate.Finish(sess.ID, flight, nil)

	_, _, err = st.Submissions.PutPromptSubmission(t.Context(), store.PromptSubmission{
		ID: uuid.NewString(), SessionID: sess.ID, ProjectID: sess.ProjectID,
		InputDigest: "u1", InputJSON: `{}`, Origin: store.PromptSubmissionOriginUser, SubmittedBy: sess.OwnerPersonID,
	})
	testutil.FailErr(t, "queue user prompt", err)
	if ok, retry := svc.AmbientWakeEligible(t.Context(), sess.ID); ok || !retry {
		t.Fatalf("queued user prompt = (%v, %v), want (false, true)", ok, retry)
	}
	testutil.FailErr(t, "clear user prompt", st.Submissions.InterruptPromptSubmissionsBySession(t.Context(), sess.ID))

	q.AppendOrdered(sess.ID, "draft-1", sess.OwnerPersonID, "pending", 1, time.Now().UTC())
	if ok, retry := svc.AmbientWakeEligible(t.Context(), sess.ID); ok || !retry {
		t.Fatalf("queued draft = (%v, %v), want (false, true)", ok, retry)
	}
	q.CancelAll(sess.ID)

	if ok, retry := svc.AmbientWakeEligible(t.Context(), sess.ID); !ok || retry {
		t.Fatalf("idle eligible session = (%v, %v), want (true, false)", ok, retry)
	}
}

func TestAmbientWatchWakeAdmitsAndRunsWhenEligible(t *testing.T) {
	st := openCompletionStore(t)
	sess, err := st.Sessions.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	blocked := true
	admitted := 0
	executed := 0
	svc := New(
		st.Sessions,
		st.Submissions,
		st.People,
		queue.New(),
		lifecycle.New(st.Sessions),
		nil,
		nil,
		nil,
		&submissionstate.Service{},
		&promptstate.MutexRegistry{},
		func(_ context.Context, _ string, _ promptinput.Input, lock *promptstate.Lock) (*promptresult.Result, error) {
			defer lock.Unlock()
			executed++
			return &promptresult.Result{}, nil
		},
		nil,
		nil,
		func(context.Context, string) bool { return blocked },
	)

	res, err := svc.AmbientWatchWake(t.Context(), sess.ID, uuid.NewString(), "watch fired", func() error {
		admitted++
		return nil
	})
	testutil.FailErr(t, "ineligible ambient wake", err)
	if res != nil || admitted != 0 || executed != 0 {
		t.Fatalf("ineligible wake ran: res=%v admitted=%d executed=%d", res, admitted, executed)
	}

	blocked = false
	subID := uuid.NewString()
	res, err = svc.AmbientWatchWake(t.Context(), sess.ID, subID, "watch fired", func() error {
		admitted++
		return nil
	})
	testutil.FailErr(t, "eligible ambient wake", err)
	if res == nil || admitted != 1 || executed != 1 {
		t.Fatalf("eligible wake = (res=%v, admitted=%d, executed=%d), want (non-nil, 1, 1)", res, admitted, executed)
	}
}
