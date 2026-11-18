package testutil

import (
	"strings"
	"sync/atomic"
	"testing"
)

func pendingWaitReport() string {
	var report strings.Builder
	writePendingWaits(&report)
	return report.String()
}

func TestWaitIsReportedByTestAndStepOnlyWhileItIsPending(t *testing.T) {
	line := "pending wait: " + t.Name() + ": the release closes\n"
	release := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		AwaitClosed(t, "the release closes", release)
	}()
	WaitFor(t, "the wait is listed", func() bool { return strings.Contains(pendingWaitReport(), line) })
	close(release)
	<-returned
	if report := pendingWaitReport(); strings.Contains(report, line) {
		t.Fatalf("finished wait still listed:\n%s", report)
	}
}

func TestWaitForReturnsOnceTheConditionHolds(t *testing.T) {
	var polls atomic.Int32
	WaitFor(t, "the third poll", func() bool { return polls.Add(1) == 3 })
	if got := polls.Load(); got != 3 {
		t.Fatalf("polls = %d, want 3", got)
	}
}

func TestReceiveReturnsTheSentValue(t *testing.T) {
	values := make(chan int, 1)
	values <- 7
	if got := Receive(t, "the value arrives", values); got != 7 {
		t.Fatalf("Receive = %d, want 7", got)
	}
}
