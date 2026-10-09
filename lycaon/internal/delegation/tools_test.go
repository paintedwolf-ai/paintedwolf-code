package delegation

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDelegateInitCreatesDelegation(t *testing.T) {
	delStore := NewMemoryStore()
	queue := worker.NewInMemoryQueue(2)
	sessStore := store.NewMemory()
	mgr := session.NewHost(sessStore, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	delegationMgr := NewManager(delStore, queue, mgr, nil)
	regProj := project.NewMemoryRegistry()
	delegationMgr.Projects = regProj

	reg := tools.NewDefaultRegistry()
	if err := RegisterDelegationTools(reg, delegationMgr); err != nil {
		testutil.FailErr(t, "RegisterDelegationTools failed", err)
	}

	dir := t.TempDir()
	p, err := project.CreateWithRoot(context.Background(), regProj, dir)
	testutil.FailErr(t, "project.CreateWithRoot failed", err)
	sess, err := sessStore.Create(context.Background(), api.CreateSessionRequest{Posture: api.SessionPostureBuild, ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "sessStore.Create failed", err)

	raw, err := reg.Run(context.Background(), "delegate_init", map[string]any{"task": "ship it"}, tools.ToolContext{
		ProjectID:    p.ID,
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
		ActiveRootID: "r1",
		SessionID:    sess.ID,
	})
	testutil.FailErr(t, "reg.Run failed", err)
	if raw == "" {
		t.Fatal("empty response")
	}
	delegationID, ok := delStore.DelegationBySessionID(sess.ID)
	if !ok {
		t.Fatal("delegation not linked to session")
	}
	got, err := delStore.Get(context.Background(), delegationID)
	testutil.FailErr(t, "store.Get failed", err)
	if got.Task != "ship it" {
		t.Fatalf("task = %q", got.Task)
	}
}
