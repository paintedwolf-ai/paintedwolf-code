package bgprocess_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
)

// admittedDirectIPLaunch binds the fixture's actual action lease and carries its
// live recheck as the agent launch's current authority, as tool producers do.
func admittedDirectIPLaunch(t *testing.T, subject, sessionID, callID, dir string) exec.LaunchPlan {
	t.Helper()
	boundary := &confine.Confinement{Roots: []string{dir}, Network: confine.NetworkDirectIP}
	identity := confine.EgressCommand{SessionID: sessionID, ToolCallID: callID, ProjectDir: dir}
	lease, err := confine.BindAction(boundary, identity)
	testutil.FailErr(t, "admit direct-ip action", err)
	t.Cleanup(func() { lease.Close(context.Background()) })
	plan := exec.AgentLaunch(exec.LaunchAgentCommand, subject, boundary)
	plan.CurrentAuthority = func(ctx context.Context) error { return lease.ValidateLaunch(ctx, boundary, identity) }
	return plan
}

func TestActiveDirectIPJobsActiveUnknownAndCompleted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	dir := t.TempDir()
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{
		Launch:     admittedDirectIPLaunch(t, "test", "sess-1", "call-direct", dir),
		ProjectDir: dir,
		Stages:     []exec.Stage{{Name: "sleep", Args: []string{"30"}}},
	}, runner)
	testutil.FailErr(t, "start direct-ip background", err)

	jobs := reg.ActiveDirectIPJobs("sess-1")
	if len(jobs) != 1 || jobs[0].Handle != handle || jobs[0].Status != bgprocess.JobLivenessActive {
		t.Fatalf("active jobs = %+v", jobs)
	}

	testutil.FailErr(t, "mark unknown", reg.MarkDirectIPLivenessUnknown("sess-1", handle))
	jobs = reg.ActiveDirectIPJobs("sess-1")
	if len(jobs) != 1 || jobs[0].Status != bgprocess.JobLivenessUnknown {
		t.Fatalf("unknown jobs = %+v", jobs)
	}

	_, err = reg.Lifecycle.Stop("sess-1", handle)
	testutil.FailErr(t, "stop", err)
	for t.Context().Err() == nil {
		if len(reg.ActiveDirectIPJobs("sess-1")) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("completed direct-ip job must leave protection set, got %+v", reg.ActiveDirectIPJobs("sess-1"))
}

func TestActiveDirectIPJobsOmitsNonDirect(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	_, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{
		ProjectDir: t.TempDir(),
		Stages:     []exec.Stage{{Name: "sleep", Args: []string{"2"}}},
	}, runner)
	testutil.FailErr(t, "start ordinary background", err)
	if jobs := reg.ActiveDirectIPJobs("sess-1"); len(jobs) != 0 {
		t.Fatalf("non-direct jobs must be omitted, got %+v", jobs)
	}
}
