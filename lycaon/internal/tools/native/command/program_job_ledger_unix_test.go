//go:build unix

package command_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// exitedLedgerJob waits for the session's one & job to end and returns its
// ledger row, or false when the ledger no longer lists it.
func exitedLedgerJob(t *testing.T, f *programFixture, handle string) (bgprocess.JobSnapshot, bool) {
	t.Helper()
	_, _, err := f.Background.Lifecycle.Wait(t.Context(), f.Session, handle)
	testutil.FailErr(t, "wait for & job", err)
	for _, job := range f.Background.LedgerJobs(f.Session) {
		if job.Handle == handle {
			return job, true
		}
	}
	return bgprocess.JobSnapshot{}, false
}

// onlyLedgerJob is the session's one & job, running or exited.
func onlyLedgerJob(t *testing.T, f *programFixture) string {
	t.Helper()
	jobs := f.Background.LedgerJobs(f.Session)
	if len(jobs) != 1 {
		t.Fatalf("expected one & job, ledger %+v", jobs)
	}
	return jobs[0].Handle
}

func TestExitedJobStaysInTheLedgerUntilItsOutputIsRead(t *testing.T) {
	f := newProgramFixture(t)
	_, err := f.call(t, t.Context(), "external -- /bin/sh -c 'exit 3' &")
	testutil.FailErr(t, "launch & job", err)
	handle := onlyLedgerJob(t, f)
	job, listed := exitedLedgerJob(t, f, handle)
	if !listed || !job.Exited || job.ExitCode != 3 {
		t.Fatalf("unobserved exit missing from ledger: %+v listed=%v", job, listed)
	}
	reader := &command.CommandOutputTool{Registry: f.Background}
	_, err = reader.Run(t.Context(), map[string]any{"handle": handle}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: f.Session}})
	testutil.FailErr(t, "read job output", err)
	if _, listed := exitedLedgerJob(t, f, handle); listed {
		t.Fatal("read job stayed in the ledger")
	}
}

func TestJobWaitedInTheShellNeverReachesTheLedger(t *testing.T) {
	f := newProgramFixture(t)
	out, err := f.call(t, t.Context(), "external -- /bin/sh -c 'exit 3' &\nwait $!")
	testutil.FailErr(t, "launch and wait & job", err)
	if outcome := decodeOutcome(t, out); outcome.Status != 3 {
		t.Fatalf("wait status = %d, want the job's exit", outcome.Status)
	}
	if jobs := f.Background.LedgerJobs(f.Session); len(jobs) != 0 {
		t.Fatalf("waited job listed as an unobserved exit: %+v", jobs)
	}
}
