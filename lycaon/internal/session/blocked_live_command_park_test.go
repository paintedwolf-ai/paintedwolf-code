package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	hostexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"runtime"
	"testing"
	"time"
)

func TestParkBlockedLiveCommandsArmsExactProcessSubscription(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep fixture is unix-oriented")
	}
	ctx := context.Background()
	reg := bgprocess.NewRegistry(bgprocess.Config{}, bgprocess.Hooks{})
	t.Cleanup(func() {
		testutil.FailErr(t, "dispose process", reg.DisposeSession(ctx, "session-1"))
	})
	mgr := NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetBackgroundRegistry(reg)
	handle, err := reg.StartPipeline(ctx, bgprocess.PipelineSpec{
		SessionID:  "session-1",
		ProjectID:  "project-1",
		Mode:       bgprocess.JobModeBackground,
		OriginTool: "command",
		Timeout:    5 * time.Second,
		Runner:     hostcmd.NewRunner(),
		Request: hostcmd.Request{
			Launch:     hostexec.HostLaunch("blocked live command park test"),
			ProjectDir: t.TempDir(),
			Stages:     []hostexec.Stage{{Name: "sleep", Args: []string{"2"}}},
		},
	})
	testutil.FailErr(t, "start process", err)

	if !mgr.parkBlockedLiveCommands(ctx, "session-1") {
		t.Fatal("running visible command was not parked")
	}
	loop := mgr.ensureCoordinatorRuntime().CoordinatorLoop()
	if !loop.Waits.IsSleeping("session-1") {
		t.Fatal("coordinator loop is not sleeping")
	}
	triggers := loop.Subscriptions.Triggers("session-1")
	if len(triggers) != 2 || triggers[0] != loopwake.WaitTriggerTimer || triggers[1] != loopwake.WaitTriggerProcessDone {
		t.Fatalf("wait triggers = %v want [timer process_done]", triggers)
	}
	handles := loop.Subscriptions.ActiveProcessHandles("session-1")
	if len(handles) != 1 || handles[0] != handle {
		t.Fatalf("wait handles = %v want [%s]", handles, handle)
	}
}

func TestParkBlockedLiveCommandsDoesNothingWithoutVisibleJob(t *testing.T) {
	mgr := NewManager(store.NewMemory(), nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	mgr.SetBackgroundRegistry(bgprocess.NewRegistry(bgprocess.Config{}, bgprocess.Hooks{}))
	if mgr.parkBlockedLiveCommands(context.Background(), "session-1") {
		t.Fatal("empty process registry must not arm a wait")
	}
}
