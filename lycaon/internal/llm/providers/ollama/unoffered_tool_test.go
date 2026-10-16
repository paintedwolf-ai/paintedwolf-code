package ollama

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// The shipped tool_not_offered grammar is Ollama's exact rejection with a
// function-name token.
func TestUnofferedToolErrorGrammar(t *testing.T) {
	policy := shippedToolCallRejections(t)
	req := Request{Tools: toolsToOllama([]tools.ToolMeta{{Name: "read"}, {Name: "ask_user"}})}
	for errText, want := range map[string]string{
		"tool 'edit' not found":                                 "edit",
		"tool 'mcp_git-hub_open_pr' not found":                  "mcp_git-hub_open_pr",
		"tool '" + strings.Repeat("a", 64) + "' not found":      strings.Repeat("a", 64),
		"tool '" + strings.Repeat("a", 65) + "' not found":      "",
		"tool 'read' not found":                                 "",
		"tool '' not found":                                     "",
		"tool 'ed it' not found":                                "",
		"tool 'a'b' not found":                                  "",
		"tool 'edit\n' not found":                               "",
		"tool \"edit\" not found":                               "",
		" tool 'edit' not found":                                "",
		"tool 'edit' not found ":                                "",
		"Tool 'edit' not found":                                 "",
		"tool 'edit' not found: retry":                          "",
		"model 'edit' not found":                                "",
		"unexpected end of JSON input":                          "",
		`invalid character '\'' in string escape code`:          "",
		"tool 'edit' not found\ntool 'write' not found":         "",
		"error parsing tool call: tool 'edit' not found":        "",
		"tool 'edit' not found; tool 'surface_snapshot' exists": "",
	} {
		tc, ok, _ := req.toolCallRejection(policy, "desktop", errText)
		if got := map[bool]string{true: tc.Name}[ok]; got != want {
			t.Errorf("%q recovered %q, want %q", errText, got, want)
		}
		if ok && (tc.ID == "" || len(tc.Args) != 0 || tc.ArgsMalformed || tc.ArgsTruncated) {
			t.Errorf("%q recovered call %+v, want a bare named call", errText, tc)
		}
	}
	if _, ok, _ := (Request{}).toolCallRejection(policy, "desktop", "tool 'edit' not found"); ok {
		t.Fatal("recovered without an offered set")
	}
}

func TestOllamaRecoversUnofferedToolRejection(t *testing.T) {
	read := tools.ToolMeta{Name: "read", ArgsSchema: map[string]any{"type": "object"}}
	ask := tools.ToolMeta{Name: "ask_user", ArgsSchema: map[string]any{"type": "object"}}
	cases := []struct {
		name          string
		tools         []tools.ToolMeta
		forbidden     bool
		format        bool
		providerError string
		recovers      bool
	}{
		{name: "unoffered name", tools: []tools.ToolMeta{read, ask}, providerError: "tool 'edit' not found", recovers: true},
		{name: "no tools", providerError: "tool 'edit' not found"},
		{name: "forbidden", tools: []tools.ToolMeta{read}, forbidden: true, providerError: "tool 'edit' not found"},
		{name: "structured", tools: []tools.ToolMeta{read}, format: true, providerError: "tool 'edit' not found"},
		{name: "offered name", tools: []tools.ToolMeta{read, ask}, providerError: "tool 'read' not found"},
		{name: "anonymous syntax", tools: []tools.ToolMeta{read}, providerError: "unexpected end of JSON input"},
		{name: "anonymous escape", tools: []tools.ToolMeta{read}, providerError: "invalid character 'x' in string escape code"},
	}
	for _, tc := range cases {
		// Ollama sends the error in-band after output and as HTTP 500 before any.
		for _, beforeOutput := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(tc.name+map[bool]string{false: "/after output", true: "/before output"}[beforeOutput]+map[bool]string{false: "/complete", true: "/stream"}[stream], func(t *testing.T) {
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var payload Request
						_ = json.NewDecoder(r.Body).Decode(&payload)
						if beforeOutput {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusInternalServerError)
						} else if payload.Stream {
							w.Header().Set("Content-Type", "application/x-ndjson")
							_, _ = io.WriteString(w, `{"message":{"content":"Reading first."},"done":false}`+"\n")
							_, _ = io.WriteString(w, `{"message":{"tool_calls":[{"function":{"name":"read","arguments":{"path":"a.go"}}}]},"done":false}`+"\n")
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"error": tc.providerError})
					}))
					defer srv.Close()
					p := New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "ministral-3:3b", ContextLength: 32768}}).WithToolCallRejections(shippedToolCallRejections(t))
					req := modelcall.CompletionRequest{Model: "ministral-3:3b", Tools: tc.tools, Messages: []api.Message{{Role: api.MessageRoleUser, Content: "give me the gist"}}}
					if tc.forbidden {
						req.ToolUse = modelcall.ToolUseForbidden
					}
					if tc.format {
						req.ResponseFormat = &modelcall.ResponseFormat{Type: "json_object"}
					}
					out, err := completeOrCollect(t, p, req, stream)
					if !tc.recovers {
						if err == nil || !strings.Contains(err.Error(), tc.providerError) {
							t.Fatalf("error = %v, want original provider error %q", err, tc.providerError)
						}
						if out != nil && len(out.ToolCalls) != 0 {
							t.Fatalf("failed response exposed calls: %+v", out.ToolCalls)
						}
						return
					}
					if err != nil {
						t.Fatalf("recoverable rejection failed the turn: %v", err)
					}
					var names []string
					for _, call := range out.ToolCalls {
						names = append(names, call.Name)
					}
					want := "edit"
					if stream && !beforeOutput {
						want = "read,edit"
					}
					if strings.Join(names, ",") != want {
						t.Fatalf("calls = %v, want %s", names, want)
					}
					rejected := out.ToolCalls[len(out.ToolCalls)-1]
					for _, offered := range tc.tools {
						if offered.Name == rejected.Name {
							t.Fatalf("recovered call %q is offered and would execute", rejected.Name)
						}
					}
					if len(rejected.Args) != 0 {
						t.Fatalf("recovered call carried args %v", rejected.Args)
					}
				})
			}
		}
	}
}

func completeOrCollect(t *testing.T, p *Provider, req modelcall.CompletionRequest, stream bool) (*modelcall.Completion, error) {
	t.Helper()
	if !stream {
		return p.Complete(t.Context(), req)
	}
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		return nil, err
	}
	out, _, err := modelcall.CollectStream(ch)
	return out, err
}
