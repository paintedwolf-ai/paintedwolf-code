package session_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type failingSecretInheritanceStore struct{ *store.Memory }

func (*failingSecretInheritanceStore) SeedSecretExposure(context.Context, string) error {
	return errors.New("evidence store unavailable")
}

func TestReadSensitivePathSetsSecretExposure(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)

	dir := t.TempDir()
	_, _, err = mem.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": "main.go"}, `{"path":"main.go","content":"package main"}`)
	testutil.FailErr(t, "read main.go", err)
	exposed, err := mem.SessionSecretExposure(ctx, sess.ID)
	testutil.FailErr(t, "read secret exposure after source read", err)
	if exposed {
		t.Fatal("reading main.go must not set secret exposure")
	}

	_, _, err = mem.CommitEvidenceToolResult(ctx, sess.ID, dir, "read", map[string]any{"path": ".env"}, `{"path":".env","content":"KEY=x"}`)
	testutil.FailErr(t, "read .env", err)
	exposed, err = mem.SessionSecretExposure(ctx, sess.ID)
	testutil.FailErr(t, "read secret exposure after credential read", err)
	if !exposed {
		t.Fatal("reading .env must set secret exposure")
	}
}

func TestSecretExposureInheritAndMerge(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	mgr := session.NewManager(mem, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	testutil.FailErr(t, "seed parent", mem.SeedSecretExposure(ctx, parent.ID))
	exposed, err := mem.SessionSecretExposure(ctx, parent.ID)
	testutil.FailErr(t, "read parent exposure", err)
	if !exposed {
		t.Fatal("parent should be secret-exposed after seed")
	}

	child, err := mgr.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "implementer", Prompt: "do work"})
	testutil.FailErr(t, "SpawnChild", err)
	exposed, err = mem.SessionSecretExposure(ctx, child.ID)
	testutil.FailErr(t, "read child exposure", err)
	if !exposed {
		t.Fatal("spawned child must inherit secret exposure")
	}

	parent2, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent2", err)
	child2, err := mem.CreateChild(ctx, parent2, api.SpawnChildRequest{AgentType: "implementer", Prompt: "x"})
	testutil.FailErr(t, "create child2", err)
	testutil.FailErr(t, "seed child2", mem.SeedSecretExposure(ctx, child2.ID))
	testutil.FailErr(t, "merge", session.MergeWorkerSecretExposureIntoParent(ctx, mem, parent2.ID, child2.ID))
	exposed, err = mem.SessionSecretExposure(ctx, parent2.ID)
	testutil.FailErr(t, "read merged parent exposure", err)
	if !exposed {
		t.Fatal("parent must pick up child secret exposure on merge")
	}
}

func TestSpawnChildRollsBackWhenSecurityInheritanceFails(t *testing.T) {
	ctx := t.Context()
	mem := store.NewMemory()
	parent, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	testutil.FailErr(t, "seed parent", mem.SeedSecretExposure(ctx, parent.ID))
	failing := &failingSecretInheritanceStore{Memory: mem}
	mgr := session.NewManager(failing, llm.NewMockProvider(&llm.MockConfig{}), tools.NewStubRegistry(), settings.DefaultSessionLimits())

	if _, err := mgr.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "implementer"}); err == nil {
		t.Fatal("SpawnChild succeeded without inheriting secret exposure")
	}
	sessions, err := mem.List(ctx)
	testutil.FailErr(t, "list sessions", err)
	if len(sessions) != 1 || sessions[0].ID != parent.ID {
		t.Fatalf("sessions after failed spawn = %+v", sessions)
	}
}

func TestSessionSecretExposureSQLRebuild(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "secret.db")
	hot := store.NewSQL(sqlDB)
	ctx := context.Background()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := hot.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)

	testutil.FailErr(t, "seed", hot.SeedSecretExposure(ctx, sess.ID))
	cold := store.NewSQL(sqlDB)
	exposed, err := cold.SessionSecretExposure(ctx, sess.ID)
	testutil.FailErr(t, "rebuild secret exposure", err)
	if !exposed {
		t.Fatal("SQL rebuild-from-records must report secret exposure")
	}
}
