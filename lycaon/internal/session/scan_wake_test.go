package session

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/session/coordinatorcontrol"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNudgeCoordinatorScanDoneWakesOnlyTheSessionThatRequestedTheScan(t *testing.T) {
	store := store.NewMemory()
	mgr := NewManager(store, nil, nil, settings.DefaultSessionLimits())

	requester, err := store.Create(context.Background(), api.CreateSessionRequest{ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "create requesting session", err)
	sameProject, err := store.Create(context.Background(), api.CreateSessionRequest{ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "create same-project session", err)

	mgr.Coordinator.Scans.Wait = coordinatorcontrol.ScanWaitState{
		Requested: func(_ context.Context, sessionID, scanID string) bool {
			return sessionID == requester.ID && scanID == "scan-1"
		},
	}
	loop := mgr.ensureCoordinatorRuntime().CoordinatorLoop()
	deadline := time.Now().UTC().Add(10 * time.Minute)
	scanTriggers := []loopwake.WaitTrigger{loopwake.WaitTriggerTimer, loopwake.WaitTriggerScanDone}
	loop.EnterSleep(context.Background(), requester.ID, deadline, "waiting for scan", scanTriggers, nil, loopwake.SleepMoverHost)
	loop.EnterSleep(context.Background(), sameProject.ID, deadline, "waiting for scan", scanTriggers, nil, loopwake.SleepMoverHost)

	mgr.Coordinator.Scans.Finished(context.Background(), api.CodeScan{
		ID:            "scan-1",
		CanonicalPath: "/tmp/project-a",
		Status:        api.CodeScanStatusComplete,
	})

	if loop.IsSleeping(requester.ID) {
		t.Fatal("scan completion must wake the session that requested the scan")
	}
	if !loop.IsSleeping(sameProject.ID) {
		t.Fatal("scan completion must not wake a session in the same project that did not request it")
	}
}
