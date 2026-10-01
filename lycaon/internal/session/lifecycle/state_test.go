package lifecycle

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStopAdmissionIsLinearizedPerSessionTree(t *testing.T) {
	mgr := &State{}
	first, second := "first", "second"

	admitted := make(chan struct{})
	release := make(chan struct{})
	admissionDone := make(chan error, 1)
	go func() {
		admissionDone <- mgr.WithAdmission(first, func() error {
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
