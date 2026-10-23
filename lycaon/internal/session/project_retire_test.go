package session

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

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

	archived, err := mem.Create(ctx, api.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create archived session", err)
	testutil.FailErr(t, "archive session", mem.UpdateSession(ctx, archived.ID, func(s *api.Session) { now := time.Now(); s.ArchivedAt = &now }))
	hub := events.NewMemoryHub()
	src := &attention.Source{Sessions: mem}
	pub := &events.Publisher{Hub: hub, Attention: src}
	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetEventPublisher(pub)

	view, err := src.BuildView(ctx)
	testutil.FailErr(t, "attention before", err)
	if !attentionHasSession(view, doomed.ID) {
		t.Fatal("errored session must appear on attention before project delete")
	}

	var progress [][2]int
	testutil.FailErr(t, "retire", mgr.Chats.RetireForProjectDelete(ctx, p.ID, func(done, total int) { progress = append(progress, [2]int{done, total}) }))
	if !reflect.DeepEqual(progress, [][2]int{{0, 3}, {2, 3}, {3, 3}}) {
		t.Fatalf("chat progress = %v", progress)
	}
	if _, err := mem.Get(ctx, archived.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("archived chat survived: %v", err)
	}

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

	mgr := NewHost(mem, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	if err := mgr.Chats.Delete(ctx, sess.ID); !errors.Is(err, chats.ErrSessionWorktreeBound) {
		t.Fatalf("DeleteSession = %v, want chats.ErrSessionWorktreeBound", err)
	}
	testutil.FailErr(t, "retire", mgr.Chats.RetireForProjectDelete(ctx, "p1", nil))
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
