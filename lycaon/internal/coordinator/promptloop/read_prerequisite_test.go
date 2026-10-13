package promptloop_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type retryReadDocuments struct {
	tools.EditorDocuments
	reads map[string]int64
}

func (d retryReadDocuments) FreezeAgentReads(string, string) map[string]int64 { return d.reads }

func TestToolPreflightResolvesOnlyItsFrozenReadPrerequisite(t *testing.T) {
	for _, document := range []string{"required", "other", ""} {
		t.Run(document, func(t *testing.T) {
			ctx := t.Context()
			guard := loopguard.NewMemoryDoomLoopGuard()
			messages := store.NewMemory()
			sess, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create chat", err)
			args := map[string]any{"path": "a.txt", "old_string": "base", "new_string": "changed"}
			for response := range loopguard.DoomLoopMaxSameCodeRejects {
				failure := &api.InvocationFailure{Code: "EDITOR_DOCUMENT_READ_REQUIRED", Details: map[string]any{"document_id": "required"}}
				testutil.FailErr(t, "record prerequisite", guard.RecordAttempt(ctx, sess.ID, fmt.Sprint(response), "edit", args, failure, false))
			}
			deps := promptloop.StoreDeps(messages)
			deps.Context.Tools, deps.Context.Policy, deps.Nudges.DoomLoop = tools.NewStubRegistry(), &fixedToolPolicy{metas: []tools.ToolMeta{{Name: "edit", ArgsSchema: map[string]any{"type": "object"}}}}, guard
			deps.Context.LoadedTools = func(string) map[string]bool { return map[string]bool{"edit": true} }
			invoked := 0
			testutil.FailErr(t, "register edit", deps.Context.Tools.Register("edit", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
				invoked++
				if revision, ok := tctx.Source.EditorReadBases.Lookup("required"); !ok || revision != 2 {
					t.Fatal("edit received no matching frozen read")
				}
				return "Edited.", nil
			}))
			deps.Model.LLM = llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".*", FollowUpText: "Finished.", ToolCalls: []llm.MockToolCall{{ID: "retry-edit", Name: "edit", Args: args}}}}})
			_, err = promptloop.NewPromptLoopForTest(deps).Run(ctx, promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("retry the edit after reading"), ProfileID: "coordinator", ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID, ProjectID: testdbseed.DefaultProjectID}, Source: tools.InvocationSource{EditorDocuments: retryReadDocuments{reads: map[string]int64{document: 2}}}}})
			testutil.FailErr(t, "run recovered invocation", err)
			want := 0
			if document == "required" {
				want = 1
			}
			if invoked != want {
				t.Fatalf("edit invoked %d times, want %d", invoked, want)
			}
		})
	}
}
