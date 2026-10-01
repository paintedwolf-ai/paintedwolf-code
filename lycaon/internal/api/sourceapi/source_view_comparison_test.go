package sourceapi

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestAcquireRetainedSourceInSameWorkspace(t *testing.T) {
	server := newSourceHandlerFixture(t)
	scope := pagedview.Scope{Person: "person-1", Project: "project-1"}
	workspaceID := "ws-shared-1"

	held := &sourceView{scope: scope, workspaceID: workspaceID, sessionID: "chat-session-1", state: "ready"}
	id, err := server.sourceViewRegistry().registry.Put(scope, held, 1, func(*sourceView) {})
	testutil.FailErr(t, "put held view", err)
	held.id = id

	// Unscoped file views share retained sources within the workspace.
	unscopedView := &sourceView{id: uuid.NewString(), scope: scope, workspaceID: workspaceID, sessionID: ""}
	source := &wire.RetainedComparisonSource{ViewID: held.id, Comparison: "current"}

	acquired, release, err := server.acquireRetainedSource(unscopedView, source)
	testutil.FailErr(t, "acquire retained source across session and unscoped view in same workspace", err)
	if acquired.id != held.id {
		t.Fatalf("acquired view id = %q, want %q", acquired.id, held.id)
	}
	release()

	// Peer chats in unbranched workspaces share retained sources.
	peerChatView := &sourceView{id: uuid.NewString(), scope: scope, workspaceID: workspaceID, sessionID: "chat-session-2"}
	acquiredPeer, releasePeer, err := server.acquireRetainedSource(peerChatView, source)
	testutil.FailErr(t, "acquire retained source across peer chats in same workspace", err)
	if acquiredPeer.id != held.id {
		t.Fatalf("acquired view id = %q, want %q", acquiredPeer.id, held.id)
	}
	releasePeer()

	otherWorkspaceView := &sourceView{id: uuid.NewString(), scope: scope, workspaceID: "ws-different", sessionID: "chat-session-1"}
	if _, _, err := server.acquireRetainedSource(otherWorkspaceView, source); err == nil {
		t.Fatal("expected error acquiring retained source from different workspace, got nil")
	}
}
