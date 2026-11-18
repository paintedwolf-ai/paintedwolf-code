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
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRetireForProjectDeleteRemovesSessionsAndAttention(t *testing.T) {
	sessionFixture := storetest.New(t, "other")

	ctx := context.Background()
	sessionStore := sessionFixture
	reg := project.NewSQLRegistry(sessionFixture.DB())
	p, err := reg.Create(ctx, project.CreateParams{Draft: true, Name: "scratch"})
	testutil.FailErr(t, "create project", err)

	keep, err := sessionStore.Create(ctx, api.CreateSessionRequest{ProjectID: "other"}, "other")
	testutil.FailErr(t, "create other session", err)
	doomed, err := sessionStore.Create(ctx, api.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create doomed session", err)
	testutil.FailErr(t, "mark error", sessionStore.SetSessionStatus(ctx, doomed.ID, api.SessionStatusError))

	child, err := sessionStore.CreateChild(ctx, doomed, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)

	archived, err := sessionStore.Create(ctx, api.CreateSessionRequest{ProjectID: p.ID}, p.ID)
	testutil.FailErr(t, "create archived session", err)
	testutil.FailErr(t, "archive session", sessionStore.UpdateSession(ctx, archived.ID, func(s *api.Session) { now := time.Now(); s.ArchivedAt = &now }))
	hub := events.NewMemoryHub()
	src := &attention.Source{Sessions: sessionStore}
	pub := &events.Publisher{Hub: hub, Attention: src}
	mgr := NewHost(sessionStore, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.SetEventPublisher(pub)

	view, err := src.BuildView(ctx)
	testutil.FailErr(t, "attention before", err)
	if !attentionHasSession(view, doomed.ID) {
		t.Fatal("errored session must appear on attention before project delete")
	}

	var progress [][2]int
	testutil.FailErr(t, "retire", mgr.Chats.RetireForProjectDelete(ctx, p.ID, func(done, total int) { progress = append(progress, [2]int{done, total}) }))
	if !reflect.DeepEqual(progress, [][2]int{{0, 3}, {1, 3}, {2, 3}, {3, 3}}) {
		t.Fatalf("chat progress = %v", progress)
	}
	if _, err := sessionStore.Get(ctx, archived.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("archived chat survived: %v", err)
	}

	if _, err := sessionStore.Get(ctx, doomed.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("doomed session still present: %v", err)
	}
	if _, err := sessionStore.Get(ctx, child.ID); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("child session still present: %v", err)
	}
	if _, err := sessionStore.Get(ctx, keep.ID); err != nil {
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
	sessionStore := storetest.New(t, "p1")
	sess, err := sessionStore.Create(ctx, api.CreateSessionRequest{ProjectID: "p1"}, "p1")
	testutil.FailErr(t, "create session", err)
	testutil.FailErr(t, "bind worktree", sessionStore.PutWorktreeBinding(ctx, store.WorktreeBinding{
		SessionID:    sess.ID,
		ProjectID:    "p1",
		RepoID:       "repo",
		Toplevel:     "/repo",
		WorktreePath: "/wt",
		Branch:       "session/x",
		BaseBranch:   "main",
	}))

	mgr := NewHost(sessionStore, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	if err := mgr.Chats.Delete(ctx, sess.ID); !errors.Is(err, chats.ErrSessionWorktreeBound) {
		t.Fatalf("DeleteSession = %v, want chats.ErrSessionWorktreeBound", err)
	}
	testutil.FailErr(t, "retire", mgr.Chats.RetireForProjectDelete(ctx, "p1", nil))
	if _, err := sessionStore.Get(ctx, sess.ID); !errors.Is(err, store.ErrSessionNotFound) {
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
