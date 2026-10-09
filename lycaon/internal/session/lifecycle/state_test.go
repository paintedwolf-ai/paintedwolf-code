package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestStopAdmissionIsLinearizedPerSessionTree(t *testing.T) {
	mgr := &State{}
	first, second := "first", "second"

	admitted := make(chan struct{})
	release := make(chan struct{})
	admissionDone := make(chan error, 1)
	go func() {
		admissionDone <- mgr.WithSessionTreeAdmission(t.Context(), first, func() error {
			close(admitted)
			<-release
			return nil
		})
	}()
	<-admitted

	otherFlight, leader := mgr.Begin(second)
	if !leader {
		t.Fatal("independent session stop was not admitted")
	}
	mgr.Finish(second, otherFlight, nil)

	firstStarted := make(chan struct{})
	firstResult := make(chan *Flight, 1)
	go func() {
		close(firstStarted)
		flight, _ := mgr.Begin(first)
		firstResult <- flight
	}()
	<-firstStarted
	testutil.WaitFor(t, time.Second, func() bool {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		gate := mgr.gates[first]
		return gate != nil && gate.refs >= 2
	})
	select {
	case <-firstResult:
		t.Fatal("stop crossed an in-flight admission for the same session tree")
	default:
	}
	close(release)
	testutil.FailErr(t, "finish admission", <-admissionDone)
	flight := <-firstResult
	mgr.Finish(first, flight, nil)
}

type treeSessions map[string]*api.Session

func (s treeSessions) Get(_ context.Context, id string) (*api.Session, error) {
	if sess := s[id]; sess != nil {
		return sess, nil
	}
	return nil, errors.New("session missing")
}

func TestChildAdmissionAndTurnTokensShareTheirRootStop(t *testing.T) {
	gate := New(treeSessions{
		"root": {ID: "root"}, "child": {ID: "child", ParentSessionID: "root"},
		"grandchild": {ID: "grandchild", ParentSessionID: "child"}, "other": {ID: "other"},
	})
	token, err := gate.Capture(t.Context(), "grandchild")
	testutil.FailErr(t, "capture child turn", err)
	flight, leader := gate.Begin("root")
	if !leader || !gate.InProgress(t.Context(), "grandchild") || gate.MayDrain(token) {
		t.Fatal("root stop did not invalidate its child turn")
	}
	if err := gate.WithSessionTreeAdmission(t.Context(), "child", func() error { t.Fatal("child admitted during root stop"); return nil }); !errors.Is(err, ErrStopping) {
		t.Fatalf("child admission = %v", err)
	}
	if _, err := gate.Capture(t.Context(), "grandchild"); !errors.Is(err, ErrStopping) {
		t.Fatalf("child turn = %v", err)
	}
	testutil.FailErr(t, "admit independent tree", gate.WithSessionTreeAdmission(t.Context(), "other", func() error { return nil }))
	gate.Finish("root", flight, nil)
	if gate.MayDrain(token) {
		t.Fatal("pre-stop child token became valid again")
	}
	next, err := gate.Capture(t.Context(), "child")
	testutil.FailErr(t, "capture next child turn", err)
	if !gate.MayDrain(next) {
		t.Fatal("post-stop child turn remained blocked")
	}
}
