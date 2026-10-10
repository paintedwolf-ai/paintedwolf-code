package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerEventBoard struct{ projectID string }

func (b *workerEventBoard) BuildView(_ context.Context, projectID, _, sessionID string, level api.BoardDetailLevel) (api.BoardView, error) {
	b.projectID = projectID
	return api.BoardView{SessionID: sessionID, DetailLevel: level}, nil
}

func TestWorkerWritePublishesBoardByProjectIdentity(t *testing.T) {
	hub := events.NewMemoryHub()
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Project: testdbseed.DefaultProjectID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to project events", err)
	defer unsubscribe()
	board := &workerEventBoard{}
	manager := NewHost(sessionstore.NewMemory(), Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	manager.SetEventPublisher(&events.Publisher{Hub: hub, Board: board})
	manager.Workers.Workspaces.AfterWorkerWrite(t.Context(), tools.ToolContext{Identity: tools.InvocationIdentity{ProjectID: testdbseed.DefaultProjectID, HandoffSessionID: "session"}}, "file.go")
	hub.FlushDebounced()
	if board.projectID != testdbseed.DefaultProjectID {
		t.Fatalf("board project = %q", board.projectID)
	}
	select {
	case event := <-ch:
		if event.Scope.ProjectID != testdbseed.DefaultProjectID || event.Topic != api.EventTopicBoard {
			t.Fatalf("board event = %+v", event)
		}
	default:
		t.Fatal("worker write did not refresh its project board")
	}
}
