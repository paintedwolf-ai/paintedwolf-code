package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestMemoryStoreCreateGet(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)
	testdbseed.BindSessionWorkspace(t, store, sess.ID, "/tmp/test")
	got, err := store.Get(ctx, sess.ID)
	testutil.FailErr(t, "get", err)
	if got.WorkspacePath != "/tmp/test" {
		t.Fatalf("workspace_path = %q", got.WorkspacePath)
	}
	if sess.ID == "" {
		t.Fatal("expected session ID")
	}
	if sess.Posture != api.SessionPostureBuild {
		t.Fatalf("mode = %q", sess.Posture)
	}
	if sess.Status != api.SessionStatusIdle {
		t.Fatalf("status = %q", sess.Status)
	}
	if got.ID != sess.ID {
		t.Fatalf("id mismatch: %q vs %q", got.ID, sess.ID)
	}
}

func TestMemoryStoreCreateChildPersistsAgentType(t *testing.T) {
	store := NewMemory()
	ctx := t.Context()
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	if _, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{}); err == nil {
		t.Fatal("expected blank agent_type to fail")
	}
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: " implementer "})
	testutil.FailErr(t, "create child", err)
	if child.AgentType != "implementer" {
		t.Fatalf("child agent_type = %q want implementer", child.AgentType)
	}
	stored, err := store.Get(ctx, child.ID)
	testutil.FailErr(t, "get child", err)
	if stored.AgentType != "implementer" {
		t.Fatalf("stored agent_type = %q want implementer", stored.AgentType)
	}
}

func TestMemoryStoreGetNotFound(t *testing.T) {
	store := NewMemory()
	_, err := store.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMemoryStoreList(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		_, err := store.Create(ctx, api.CreateSessionRequest{
			Posture: api.SessionPostureSpec,
		}, fmt.Sprintf("project-%d", i))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	sessions, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestMemoryStoreDelete(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()

	sess, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.Delete(ctx, sess.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, sess.ID); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestMemoryStoreDeleteNotFound(t *testing.T) {
	store := NewMemory()
	if err := store.Delete(context.Background(), "missing"); err == nil {
		t.Fatal("expected error")
	}
}

func TestMemoryStoreDeleteRemovesChildTree(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	parent, err := mem.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureBuild,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := mem.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)
	testutil.FailErr(t, "delete parent", mem.Delete(ctx, parent.ID))
	if _, err := mem.Get(ctx, child.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("child after parent delete: %v", err)
	}
}

func TestConcurrentSessionCreation(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := store.Create(ctx, api.CreateSessionRequest{
				Posture: api.SessionPostureBuild,
			}, fmt.Sprintf("project-%d", idx))
			if err != nil {
				t.Errorf("create session %d: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	sessions, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 100 {
		t.Fatalf("expected 100 sessions, got %d", len(sessions))
	}
}

func TestAppendMessagesRejectsExistingID(t *testing.T) {
	mem := NewMemory()
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create", err)
	msg := api.Message{ID: "same-id", Role: api.MessageRoleUser, Content: "first"}
	testutil.FailErr(t, "first append", mem.AppendMessages(ctx, sess.ID, msg))
	err = mem.AppendMessages(ctx, sess.ID, api.Message{ID: "same-id", Role: api.MessageRoleUser, Content: "again"})
	if !errors.Is(err, ErrDuplicateMessageID) {
		t.Fatalf("second append = %v want ErrDuplicateMessageID", err)
	}
}

func TestContextCancellation(t *testing.T) {
	store := NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := store.Create(ctx, api.CreateSessionRequest{
		Posture: api.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	if err == nil {
		t.Fatal("expected canceled context error")
	}
}
