package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDocumentReadResolvesIdenticalRejectedEdit(t *testing.T) {
	adapter, _, project, rootID := newAdapterFixture(t)
	ctx := t.Context()
	chats := store.NewMemory()
	sess, err := chats.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, project.ID)
	testutil.FailErr(t, "create chat", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "coordinator", Tools: map[string]bool{"read": true, "edit": true}}})
	toolCtx := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: project.ID, SessionID: sess.ID, Agent: "coordinator"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: rootID, Label: "root", Path: project.Roots[0].Path, IsPrimary: true}}, ActiveRootID: rootID, SourceWorkspaceKind: api.SourceWorkspaceKindProject, EditorDocuments: adapter, RepoFileCount: 1, RepoFileCountKnown: true},
	}
	edit := &native.EditTool{Boundary: boundary}
	args := map[string]any{"path": "a.txt", "old_string": "base", "new_string": "changed"}
	guard := loopguard.NewMemoryDoomLoopGuard()
	for response := range loopguard.DoomLoopMaxSameCodeRejects {
		_, err := edit.Run(ctx, args, toolCtx)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "EDITOR_DOCUMENT_READ_REQUIRED" {
			t.Fatalf("missing read refusal: %v", err)
		}
		testutil.FailErr(t, "record read prerequisite", guard.RecordAttempt(ctx, sess.ID, fmt.Sprint(response), "edit", args, &api.InvocationFailure{Code: reject.Code, Details: reject.Data}, false))
	}
	read := &survey.ReadTool{Boundary: boundary}
	readResult, err := read.Run(ctx, map[string]any{"path": "a.txt", "mode": "content"}, toolCtx)
	testutil.FailErr(t, "read document", err)
	message := api.Message{Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Content: readResult,
		ToolResult: &api.ToolResult{Tool: "read", Content: readResult, Outcome: api.ToolResultOutcomeCompleted}}
	projected := transcript.Project([]api.Message{message})
	modelResult := projected[len(projected)-1]
	if !strings.Contains(modelResult.Content, "⟦D:tool:read_metadata⟧") || !strings.Contains(modelResult.Content, "⟦D⟧base") || strings.Contains(modelResult.Content, "     1: base") {
		t.Fatalf("native read did not deliver exact source with detached metadata: %s", modelResult.Content)
	}
	for _, part := range modelResult.ContentParts {
		if part.Authority != api.ContentAuthorityNone {
			t.Fatalf("native read acquired instruction authority: %+v", part)
		}
	}
	if message.Content != readResult || message.ToolResult.Content != readResult || transcript.Project(projected)[len(projected)-1].Content != modelResult.Content {
		t.Fatal("native read projection changed durable evidence or duplicated framing")
	}
	if len(adapter.FreezeAgentReads(project.ID, sess.ID)) != 1 {
		t.Fatal("read did not establish document basis")
	}
	reads := tools.NewAgentReadBases(adapter.FreezeAgentReads(project.ID, sess.ID))
	allowed, _, code, err := guard.Check(ctx, sess.ID, "after-read", "edit", args, true, reads)
	testutil.FailErr(t, "check retry after read", err)
	if !allowed {
		t.Fatalf("resolved read still blocked with %s", code)
	}
	toolCtx.Source.EditorReadBases = tools.NewAgentReadBases(adapter.FreezeAgentReads(project.ID, sess.ID))
	_, err = edit.Run(ctx, args, toolCtx)
	testutil.FailErr(t, "apply recovered edit", err)
	text, err := os.ReadFile(filepath.Join(project.Roots[0].Path, "a.txt"))
	testutil.FailErr(t, "read accepted edit", err)
	if string(text) != "changed\n" {
		t.Fatalf("resolved prerequisite still blocks identical edit: %q", text)
	}
}
