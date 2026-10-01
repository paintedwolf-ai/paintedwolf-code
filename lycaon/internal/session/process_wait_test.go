package session

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCommandCompletionWakesWorkerProcessWait(t *testing.T) {
	memory := store.NewMemory()
	mgr := NewManager(memory, nil, nil, settings.DefaultSessionLimits())
	sess, err := memory.Create(t.Context(), api.CreateSessionRequest{}, "")
	testutil.FailErr(t, "create waiting worker session", err)
	testutil.FailErr(t, "mark worker session", memory.UpdateSession(t.Context(), sess.ID, func(s *api.Session) {
		s.ParentSessionID = "parent"
	}))
	loop := mgr.ensureCoordinatorRuntime().CoordinatorLoop()
	loop.SetDeps(loopwake.LoopDeps{GetSession: memory.Get})
	loop.EnterSleep(t.Context(), sess.ID, time.Time{}, "waiting for command completion",
		[]loopwake.WaitTrigger{loopwake.WaitTriggerProcessDone}, []string{"command-1"}, loopwake.SleepMoverHost)
	mgr.HandleCommandCompletion(t.Context(), bgprocess.Completion{SessionID: sess.ID, Handle: "other"})
	if !loop.IsSleeping(sess.ID) {
		t.Fatal("unrelated completion broke worker wait")
	}
	mgr.HandleCommandCompletion(t.Context(), bgprocess.Completion{SessionID: sess.ID, Handle: "command-1"})
	if loop.IsSleeping(sess.ID) {
		t.Fatal("worker wait ignored command completion")
	}
}
