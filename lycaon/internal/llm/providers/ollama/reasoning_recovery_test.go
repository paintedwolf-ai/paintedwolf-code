package ollama

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOllamaReservesReasoningAndOutputWithinContext(t *testing.T) {
	p := New("local-fixture", "http://unused", "", []modelinfo.Entry{{ID: "fixture", ContextLength: 8192, ThinkStyle: "boolean_think"}})
	req := modelcall.CompletionRequest{Model: "fixture", Tools: []tools.ToolMeta{{Name: "read"}}, AttemptBudget: &modelcall.CompletionBudget{}}
	out, prompt, err := p.Prepare(t.Context(), req, false)
	if err != nil {
		t.Fatalf("build small-context reasoning request: %v", err)
	}
	if out.Options.NumPredict <= 0 || prompt+out.Options.NumPredict+ollamaContextHeadroom > out.Options.NumCtx || req.AttemptBudget.MaxTokens != out.Options.NumPredict {
		t.Fatalf("unreserved reasoning/output room: prompt=%d options=%+v budget=%+v", prompt, out.Options, req.AttemptBudget)
	}
}

func TestOllamaToolContinuationReplaysReasoning(t *testing.T) {
	var received Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			_, _ = io.WriteString(w, `{"models":[]}`)
			return
		}
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		testutil.FailErr(t, "decode request", json.NewDecoder(r.Body).Decode(&received))
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"message":{"thinking":"Read the file before editing."},"done":false}`+"\n")
		_, _ = io.WriteString(w, `{"message":{"tool_calls":[{"function":{"name":"read","arguments":{"path":"a.txt"}}}]},"done":true}`+"\n")
	}))
	defer srv.Close()
	p := New("local", srv.URL, "", []modelinfo.Entry{{ID: "model", ContextLength: 131072}})
	req := modelcall.CompletionRequest{Model: "model", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Fix the file."}}}
	ch, err := p.Stream(t.Context(), req)
	testutil.FailErr(t, "open reasoning stream", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "collect reasoning stream", err)
	completion.ProviderID, completion.Model = "local", "model"
	req.Messages = append(req.Messages,
		api.Message{Role: api.MessageRoleAssistant, ToolCalls: completion.ToolCalls, ModelReasoning: completion.ModelReasoning()},
		api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "read", Content: "file text"}},
		api.Message{Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindUserContinuation, Content: "Current host state."})
	ch, err = p.Stream(t.Context(), req)
	testutil.FailErr(t, "open tool continuation", err)
	_, _, err = modelcall.CollectStream(ch)
	testutil.FailErr(t, "collect tool continuation", err)
	if len(received.Messages) != 4 || received.Messages[1].Thinking != "Read the file before editing." || len(received.Messages[1].ToolCalls) != 1 || received.Messages[2].ToolName != "read" {
		t.Fatalf("tool continuation lost reasoning or association: %+v", received.Messages)
	}
}

func TestOllamaReasoningProjectionRespectsProvenanceAndTurn(t *testing.T) {
	assistant := func(provider, model string, calls bool) api.Message {
		m := api.Message{Role: api.MessageRoleAssistant, Content: "answer", ModelReasoning: &api.ModelReasoning{ProviderID: provider, Model: model, Text: "trace"}}
		if calls {
			m.ToolCalls = []api.ToolCall{{Name: "read"}}
		}
		return m
	}
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "Previous request."},
		assistant("local", "model", true),
		{Role: api.MessageRoleUser, Content: "Current request."},
		assistant("other", "model", true), assistant("local", "other", true), assistant("", "", true),
		assistant("local", "model", false), assistant("local", "model", true),
		{Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindUserContinuation, Content: "Host feedback."},
	}
	out := ProjectMessages(msgs, MessageProjection{ProviderID: "local", Model: "model"})
	for i, m := range out {
		want := ""
		if i == 7 {
			want = "trace"
		}
		if m.Thinking != want {
			t.Fatalf("message %d thinking=%q, want %q", i, m.Thinking, want)
		}
	}
	if msgs[1].ModelReasoning.Text != "trace" {
		t.Fatal("projection changed stored reasoning")
	}
}

func TestOllamaContextAdmissionCountsReplayedReasoning(t *testing.T) {
	p := New("local", "http://unused.invalid", "", []modelinfo.Entry{{ID: "model", ContextLength: 8192}})
	req := modelcall.CompletionRequest{Model: "model", MaxTokens: 1024, Messages: []api.Message{
		{Role: api.MessageRoleUser, Content: "Continue."},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read"}}, ModelReasoning: &api.ModelReasoning{ProviderID: "local", Model: "model", Text: strings.Repeat("reasoning ", 10000)}},
	}}
	_, _, err := p.Prepare(t.Context(), req, false)
	if !errors.Is(err, failure.ErrProviderContextTooSmall) {
		t.Fatalf("reasoning context admission = %v", err)
	}
	req.Messages[1].ModelReasoning.ProviderID = "other"
	body, _, err := p.Prepare(t.Context(), req, false)
	testutil.FailErr(t, "admit history without foreign reasoning", err)
	if body.Messages[1].Thinking != "" {
		t.Fatal("foreign reasoning reached the provider")
	}
}
