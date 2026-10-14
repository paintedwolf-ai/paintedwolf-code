package history

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type nativeRequestBudgets struct{ provider *ollama.Provider }

func (b nativeRequestBudgets) RequestBudget(ctx context.Context, _ string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	return b.provider.RequestBudget(ctx, req)
}

func TestCompleteRequestFitsColdOllamaWindowWithoutChangingFrame(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Errorf("budget planning attempted inference: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()
	provider := ollama.New("local", server.URL+"/v1", "", []modelinfo.Entry{{ID: "gemma4:e4b", ContextLength: 16384}})
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{ProviderID: "local", Model: "gemma4:e4b"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	var rows []api.Message
	for i := 0; i < 8; i++ {
		rows = append(rows, api.Message{ID: fmt.Sprintf("answer-%d", i), Role: api.MessageRoleAssistant, Content: "Ready"},
			api.Message{ID: fmt.Sprintf("feedback-%d", i), Role: api.MessageRoleSystem, Content: strings.Repeat("Recorded feedback. ", 200)})
	}
	rows = append(rows, api.Message{ID: "current", Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Authority: api.ContentAuthorityUser, Content: "Recover the original observation",
		ContentParts: []api.MessageContentPart{{Content: "Recover the original observation", Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser}}})
	testutil.FailErr(t, "append canonical history", mem.AppendMessages(t.Context(), sess.ID, rows...))
	rows, err = mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read canonical history", err)
	standing := api.Message{ID: "standing", Role: api.MessageRoleSystem, Content: strings.Repeat("Standing instructions. ", 850),
		ContextPinned: true, PromptCacheBreakpoint: api.PromptCacheTierStanding}
	volatile := api.Message{ID: "volatile", Role: api.MessageRoleSystem, Content: "Live host state", ContextPinned: true}
	messages := append([]api.Message{standing}, rows...)
	messages = append(messages, volatile)
	req := modelcall.CompletionRequest{Model: sess.Model, Messages: messages, Tools: []tools.ToolMeta{{Name: "read",
		ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{
			"type": "string", "description": strings.Repeat("Recorded file path. ", 700)}}}}}}
	if _, _, err := provider.Prepare(t.Context(), req, true); !errors.Is(err, failure.ErrProviderContextTooSmall) {
		t.Fatalf("fixture must overflow before request fitting: %v", err)
	}
	cfg := compaction.DefaultCompactionConfig()
	cfg.ModelContextWindow = 12288
	history := &Service{store: mem, Compactor: compaction.NewSimpleCompactor(cfg, nil), recallAvailable: func(context.Context, *api.Session) bool { return true }}
	fitted, err := history.FitRequest(t.Context(), sess, rows, req, nativeRequestBudgets{provider})
	testutil.FailErr(t, "fit complete request", err)
	payload, _, err := provider.Prepare(t.Context(), fitted, true)
	testutil.FailErr(t, "prepare fitted native request", err)
	if payload.Options.NumCtx > 16384 || payload.Options.NumPredict <= 0 {
		t.Fatalf("invalid native reservation: %+v", payload.Options)
	}
	if !reflect.DeepEqual(fitted.Messages[0], standing) || !reflect.DeepEqual(fitted.Messages[len(fitted.Messages)-1], volatile) || !reflect.DeepEqual(fitted.Tools, req.Tools) {
		t.Fatal("history fitting changed standing, tools, cache tiers, or live host state")
	}
	current, recovery := 0, false
	for _, row := range fitted.Messages {
		if row.ID == "current" {
			current++
			if row.Content != rows[len(rows)-1].Content || !reflect.DeepEqual(row.ContentParts, rows[len(rows)-1].ContentParts) || row.Authority != api.ContentAuthorityUser {
				t.Fatal("fitting changed the original human instruction")
			}
		}
		recovery = recovery || strings.Contains(row.Content, `recall(history=`)
	}
	if current != 1 || !recovery {
		t.Fatal("fitted request lost the active instruction or canonical recovery")
	}
	canonical, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read preserved canonical history", err)
	if !reflect.DeepEqual(canonical, rows) {
		t.Fatal("request fitting changed canonical records")
	}
}

type catalogRequestBudgets struct{ catalog modelcall.Catalog }

func (b catalogRequestBudgets) RequestBudget(ctx context.Context, _ string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	return b.catalog.RequestBudget(ctx, req)
}

func TestRequestFittingKeepsUnchangedHistoryCacheBoundary(t *testing.T) {
	rows := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "go"},
		{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "read"}}},
		{ID: "t1", Role: api.MessageRoleTool, Content: "result", PromptCacheBreakpoint: api.PromptCacheTierHistory,
			ToolResult: &api.ToolResult{ToolCallID: "c1", Tool: "read", Content: "result"}},
	}
	frame := api.Message{ID: "standing", Role: api.MessageRoleSystem, Content: "Stable instructions", PromptCacheBreakpoint: api.PromptCacheTierStanding}
	req := modelcall.CompletionRequest{Model: "large", Messages: append([]api.Message{frame}, rows...)}
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "large", ContextLength: 1_000_000}})}
	history := &Service{}
	fitted, err := history.FitRequest(t.Context(), &api.Session{ID: "s", Model: "large"}, rows, req, budgets)
	testutil.FailErr(t, "fit large-window request", err)
	if !reflect.DeepEqual(req, fitted) {
		t.Fatal("an admitted frame changed its cache boundary or history")
	}
}
