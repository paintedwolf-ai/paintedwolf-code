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

func TestActiveDirectIPJobsActiveUnknownAndCompleted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based fixture is unix-oriented")
	}
	reg := newTestRegistry(t, bgprocess.Config{MaxBackground: 4}, bgprocess.Hooks{})
	runner := hostcmd.NewRunner()
	dir := t.TempDir()
	handle, err := startBackground(context.Background(), reg, "sess-1", "proj-1", hostcmd.Request{
		Launch: exec.AgentLaunch(exec.LaunchAgentCommand, "test", &confine.Confinement{
			Roots: []string{dir}, Network: confine.NetworkDirectIP,
		}),
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

	_, err = reg.Stop("sess-1", handle)
	testutil.FailErr(t, "stop", err)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
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
