package ollama

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"gopkg.in/yaml.v3"
)

// shippedToolCallRejections is the provider catalog's Ollama policy.
func shippedToolCallRejections(t *testing.T) providerprofile.ToolCallRejectionPolicy {
	t.Helper()
	data, err := config.Read(config.Providers)
	testutil.FailErr(t, "read provider catalog", err)
	var catalog struct {
		Profiles map[string]providerprofile.ToolCallRejectionPolicy `yaml:"tool_call_rejection_profiles"`
	}
	testutil.FailErr(t, "decode provider catalog", yaml.Unmarshal(data, &catalog))
	testutil.FailErr(t, "resolve rejection profiles", providerprofile.ResolveToolCallRejectionProfiles(catalog.Profiles))
	return catalog.Profiles["ollama"]
}

// The shipped grammar accepts every SyntaxError encoding/json produces for
// broken arguments, so it tracks the error Ollama's parser actually reports.
func TestToolCallSyntaxErrorGrammar(t *testing.T) {
	policy := shippedToolCallRejections(t)
	if policy.StatusBeforeOutput != http.StatusInternalServerError {
		t.Fatalf("status before output = %d, want Ollama's HTTP 500", policy.StatusBeforeOutput)
	}
	for _, raw := range []string{
		`{"path":"a.html","content":"<b>"$}`,
		`{"content":"say "hi""}`,
		`{"content":"a\qb"}`,
		`{"content":"\u12G4"}`,
		`{"path" "a"}`,
		`{path:"a"}`,
		`{"n":1.}`,
		`{"n":1e}`,
		`{"n":-x}`,
		`{"ok":trve}`,
		`{"ok":fals}`,
		`{"ok":nul}`,
		`{"a":[1 2]}`,
		`{"a":"b"} x`,
		"{\"a\":\"line\nbreak\"}",
		"{\"a\":\"tab\x01\"}",
		"{\"a\":'single'}",
		"{\"a\":\"b\"\xe9}",
		`{"a":`,
		`{"a":"b"`,
		``,
	} {
		var args map[string]any
		err := json.Unmarshal([]byte(raw), &args)
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("%q: fixture is not a syntax error: %v", raw, err)
		}
		if rejection, ok := policy.Match(err.Error()); !ok || rejection.Kind != providerprofile.ToolCallArgumentsUnparsed {
			t.Errorf("%q: grammar rejects encoding/json error %q", raw, err.Error())
		}
	}
	for _, text := range []string{
		"tool 'write' not found",
		"invalid character in string escape code",
		"invalid character '$' after object key:value pair: retry",
		" invalid character '$' after object key:value pair",
		"Invalid character '$' after object key:value pair",
		"invalid character '$$' after object key:value pair",
		"invalid character '$' after object key:value pair\ninvalid character '$' after object key",
		"invalid character '$' somewhere else",
		"json: cannot unmarshal array into Go value of type map[string]interface {}",
		"unexpected end of JSON input.",
		"model not found",
		"model runner has unexpectedly stopped",
	} {
		if rejection, ok := policy.Match(text); ok && rejection.Kind == providerprofile.ToolCallArgumentsUnparsed {
			t.Errorf("syntax grammar accepts %q", text)
		}
	}
}

func TestOllamaTypesDroppedToolCall(t *testing.T) {
	write := tools.ToolMeta{Name: "write", ArgsSchema: map[string]any{"type": "object"}}
	policy := shippedToolCallRejections(t)
	const syntaxError = "invalid character '$' after object key:value pair"
	cases := []struct {
		name          string
		tools         []tools.ToolMeta
		forbidden     bool
		format        bool
		providerError string
		typed         bool
	}{
		{name: "callable tools", tools: []tools.ToolMeta{write}, providerError: syntaxError, typed: true},
		{name: "end of input", tools: []tools.ToolMeta{write}, providerError: "unexpected end of JSON input", typed: true},
		{name: "no tools", providerError: syntaxError},
		{name: "forbidden", tools: []tools.ToolMeta{write}, forbidden: true, providerError: syntaxError},
		{name: "structured", tools: []tools.ToolMeta{write}, format: true, providerError: syntaxError},
		{name: "unrelated error", tools: []tools.ToolMeta{write}, providerError: "model not found"},
		{name: "anonymous prose", tools: []tools.ToolMeta{write}, providerError: "invalid character in string escape code"},
	}
	for _, tc := range cases {
		// Ollama sends the error in-band after output and as HTTP 500 before any.
		for _, beforeOutput := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				name := tc.name + map[bool]string{false: "/after output", true: "/before output"}[beforeOutput] + map[bool]string{false: "/complete", true: "/stream"}[stream]
				t.Run(name, func(t *testing.T) {
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var payload Request
						_ = json.NewDecoder(r.Body).Decode(&payload)
						if beforeOutput {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusInternalServerError)
						} else if payload.Stream {
							w.Header().Set("Content-Type", "application/x-ndjson")
							_, _ = io.WriteString(w, `{"message":{"content":"Writing the page."},"done":false}`+"\n")
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"error": tc.providerError})
					}))
					defer srv.Close()
					p := New("desktop", srv.URL+"/v1", "", []modelinfo.Entry{{ID: "ministral-3:3b", ContextLength: 32768}}).WithToolCallRejections(policy)
					req := modelcall.CompletionRequest{Model: "ministral-3:3b", Tools: tc.tools, Messages: []api.Message{{Role: api.MessageRoleUser, Content: "build a page"}}}
					if tc.forbidden {
						req.ToolUse = modelcall.ToolUseForbidden
					}
					if tc.format {
						req.ResponseFormat = &modelcall.ResponseFormat{Type: "json_object"}
					}
					out, err := completeOrCollectUnparsed(t, p, req, stream)
					if err == nil || !strings.Contains(err.Error(), tc.providerError) {
						t.Fatalf("error = %v, want original provider error %q", err, tc.providerError)
					}
					if out != nil && len(out.ToolCalls) != 0 {
						t.Fatalf("failed response exposed calls: %+v", out.ToolCalls)
					}
					unparsed, typed := failure.AsProviderToolCallUnparsed(err)
					if typed != tc.typed {
						t.Fatalf("typed = %v, want %v (error %v)", typed, tc.typed, err)
					}
					if typed && (unparsed.ProviderID != "desktop" || unparsed.Model != "ministral-3:3b" || err.Error() != "ollama: "+tc.providerError) {
						t.Fatalf("unparsed = %+v (%q), want provider identity and the original message", unparsed, err.Error())
					}
					if stream && !beforeOutput && (out == nil || out.Content != "Writing the page.") {
						t.Fatalf("partial = %+v, want the streamed prose", out)
					}
				})
			}
		}
	}
}

func completeOrCollectUnparsed(t *testing.T, p *Provider, req modelcall.CompletionRequest, stream bool) (*modelcall.Completion, error) {
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
