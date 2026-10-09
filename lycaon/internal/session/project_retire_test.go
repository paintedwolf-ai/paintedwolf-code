package session

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/attention"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRetireForProjectDeleteRemovesSessionsAndAttention(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true, Name: "scratch"})
	testutil.FailErr(t, "create project", err)

	keep, err := mem.Create(ctx, api.CreateSessionRequest{ProjectID: "other"}, "other")
	testutil.FailErr(t, "create other session", err)
	doomed, err := mem.Create(ctx, api.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create doomed session", err)
	testutil.FailErr(t, "mark error", mem.SetSessionStatus(ctx, doomed.ID, api.SessionStatusError))

	child, err := mem.CreateChild(ctx, doomed, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)

	hub := events.NewMemoryHub()
	src := &attention.Source{Sessions: mem}
	pub := &events.Publisher{Hub: hub, Attention: src}
	mgr := NewManager(mem, nil, nil, settings.DefaultSessionLimits())
	mgr.SetEventPublisher(pub)

	view, err := src.BuildView(ctx)
	testutil.FailErr(t, "attention before", err)
	if !attentionHasSession(view, doomed.ID) {
		t.Fatal("errored session must appear on attention before project delete")
	}

	testutil.FailErr(t, "retire", mgr.Chats.RetireForProjectDelete(ctx, p.ID))

	if _, err := mem.Get(ctx, doomed.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("doomed session still present: %v", err)
	}
	if _, err := mem.Get(ctx, child.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("child session still present: %v", err)
	}
	if _, err := mem.Get(ctx, keep.ID); err != nil {
		testutil.FailErr(t, "kept session", err)
	}

	view, err = src.BuildView(ctx)
	testutil.FailErr(t, "attention after", err)
	if attentionHasSession(view, doomed.ID) {
		t.Fatal("retired session must leave the attention view")
	}
}

func TestRetireForProjectDeleteIgnoresWorktreeBinding(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "bind worktree", mem.PutWorktreeBinding(ctx, store.WorktreeBinding{
		SessionID:    sess.ID,
		ProjectID:    "p1",
		RepoID:       "repo",
		Toplevel:     "/repo",
		WorktreePath: "/wt",
		Branch:       "session/x",
		BaseBranch:   "main",
	}))

	mgr := NewManager(mem, nil, nil, settings.DefaultSessionLimits())
	if err := mgr.Chats.Delete(ctx, sess.ID); !errors.Is(err, chats.ErrSessionWorktreeBound) {
		t.Fatalf("DeleteSession = %v, want chats.ErrSessionWorktreeBound", err)
	}
	testutil.FailErr(t, "retire", mgr.Chats.RetireForProjectDelete(ctx, "p1"))
	if _, err := mem.Get(ctx, sess.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("worktree-bound session survived project retire: %v", err)
	}
}

func attentionHasSession(view api.AttentionView, sessionID string) bool {
	for _, row := range view.Rows {
		if row.SessionID == sessionID {
			return true
		}
	}
	return false
}
