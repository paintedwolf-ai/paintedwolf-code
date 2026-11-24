package sessions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordedActivity struct {
	events chan api.ActivityEvent
}

func (r *recordedActivity) PublishActivity(_ context.Context, projectID, sessionID string, ev api.ActivityEvent) {
	if projectID != "proj-1" || sessionID != ev.SessionID {
		panic("activity published outside the waiting session")
	}
	r.events <- ev
}

func (r *recordedActivity) next(t *testing.T) api.ActivityEvent {
	t.Helper()
	select {
	case ev := <-r.events:
		return ev
	case <-t.Context().Done():
		t.Fatal("no activity published")
		return api.ActivityEvent{}
	}
}

func (r *recordedActivity) none(t *testing.T) {
	t.Helper()
	select {
	case ev := <-r.events:
		t.Fatalf("unexpected activity %+v", ev)
	default:
	}
}

// blockedCredentialWait holds the root's build and returns the activity
// recorder and the tick that stands in for the wait clock.
func blockedCredentialWait(t *testing.T) (*credentialFiles, *recordedActivity, chan time.Time, func()) {
	t.Helper()
	files := credentialFilesFixture(t)
	attachCredentialRoot(t, files)
	recorder := &recordedActivity{events: make(chan api.ActivityEvent, 8)}
	ticks := make(chan time.Time)
	files.index.events = recorder
	files.index.after = func(time.Duration) <-chan time.Time { return ticks }
	release, err := files.index.access.Acquire(t.Context(), credentialRootKey("proj-1", "r1"))
	testutil.FailErr(t, "hold root access", err)
	// The test context is already cancelled when cleanups run.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		testutil.FailErr(t, "stop index", files.closeCredentialIndex(ctx))
	})
	return files, recorder, ticks, release
}

func TestCredentialWaitShowsOneLeasePerRoot(t *testing.T) {
	files, recorder, ticks, release := blockedCredentialWait(t)
	result := make(chan error, 1)
	go func() {
		result <- files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1", SessionID: "session-1"})
	}()
	ticks <- time.Now()
	active := recorder.next(t)
	if active.Kind != api.ActivityKindIndexingCredentials || active.Status != api.ActivityStatusActive || active.SessionID != "session-1" ||
		active.Indexing == nil || active.Indexing.RootID != "r1" || active.Indexing.RootLabel != "root" {
		t.Fatalf("active lease = %+v", active)
	}
	// Unchanged counts do not republish the lease.
	ticks <- time.Now()
	recorder.none(t)
	release()
	testutil.FailErr(t, "await credentials", <-result)
	done := recorder.next(t)
	if done.ActivityID != active.ActivityID || done.Status != api.ActivityStatusDone {
		t.Fatalf("done lease = %+v, opened %s", done, active.ActivityID)
	}
	recorder.none(t)
}

func TestCredentialWaitClosesItsLeaseWhenCancelled(t *testing.T) {
	files, recorder, ticks, release := blockedCredentialWait(t)
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- files.Await(ctx, tools.CredentialWait{ProjectID: "proj-1", SessionID: "session-1"})
	}()
	ticks <- time.Now()
	active := recorder.next(t)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	done := recorder.next(t)
	if done.ActivityID != active.ActivityID || done.Status != api.ActivityStatusDone {
		t.Fatalf("done lease = %+v, opened %s", done, active.ActivityID)
	}
}

func TestCredentialWaitWithoutSessionPublishesNothing(t *testing.T) {
	files, recorder, ticks, release := blockedCredentialWait(t)
	result := make(chan error, 1)
	go func() { result <- awaitCredentials(t.Context(), files) }()
	select {
	case ticks <- time.Now():
		t.Fatal("a wait without a session armed the activity clock")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	testutil.FailErr(t, "await credentials", <-result)
	recorder.none(t)
}

func TestCredentialWaitShownAfterFastBuildPublishesNothing(t *testing.T) {
	files, recorder, _, release := blockedCredentialWait(t)
	release()
	testutil.FailErr(t, "await credentials", files.Await(t.Context(), tools.CredentialWait{ProjectID: "proj-1", SessionID: "session-1"}))
	recorder.none(t)
}
