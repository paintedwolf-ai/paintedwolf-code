package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

// Ollama's tool-call parser drops a call to a name absent from the request's
// tools and reports only fmt.Errorf("tool '%s' not found", n)
// (ollama/ollama model/parsers/ministral.go toolByName). The name is captured
// only as a provider function-name token (^[a-zA-Z0-9_-]{1,64}$).
var unofferedToolError = regexp.MustCompile(`^tool '([a-zA-Z0-9_-]{1,64})' not found$`)

// The JSON error body carries at most a 64-byte name.
const maxUnofferedToolErrorBody = 4 << 10

// unofferedToolRecoveryTools is the offered set a parser rejection is checked
// against: nil when no callable tools were sent or the request constrains
// output with a format, so the rejection stays a failure.
func (r Request) unofferedToolRecoveryTools() []ollamaTool {
	if len(r.Format) > 0 {
		return nil
	}
	return r.Tools
}

// unofferedToolCall restores the dropped call so the host answers it with
// TOOL_NOT_OFFERED, which it decides before any definition lookup. The name
// must lie outside the offered set: an offered name in this error is a
// provider fault, and no other error text becomes a call.
func unofferedToolCall(errText string, offered []ollamaTool) (api.ToolCall, bool) {
	if len(offered) == 0 {
		return api.ToolCall{}, false
	}
	match := unofferedToolError.FindStringSubmatch(errText)
	if match == nil {
		return api.ToolCall{}, false
	}
	for _, tool := range offered {
		if tool.Function.Name == match[1] {
			return api.ToolCall{}, false
		}
	}
	return api.ToolCall{ID: providerwire.NewToolCallID(), Name: match[1], Args: map[string]any{}}, true
}

// unofferedToolCompletion answers a non-stream error body: only a recoverable
// rejection becomes a completion.
func (r Request) unofferedToolCompletion(decoded ollamaChatResponse) (*modelcall.Completion, error) {
	tc, ok := unofferedToolCall(decoded.Error, r.unofferedToolRecoveryTools())
	if !ok {
		return nil, fmt.Errorf("ollama: %s", decoded.Error)
	}
	return &modelcall.Completion{
		Content:   decoded.Message.Content,
		Reasoning: decoded.Message.Thinking,
		ToolCalls: append(ollamaToolCallsToAPI(decoded.Message.ToolCalls), tc),
	}, nil
}

// admitUnofferedToolRejection reads a recoverable rejection sent before any
// output as the in-band error line it is after output: Ollama's streamResponse
// and waitForStream (server/routes.go) pick HTTP 500 only because nothing had
// been written yet.
func (r Request) admitUnofferedToolRejection(resp *http.Response, err error) (*http.Response, error) {
	offered := r.unofferedToolRecoveryTools()
	if err != nil || resp == nil || resp.StatusCode != http.StatusInternalServerError || len(offered) == 0 {
		return resp, err
	}
	peek, _ := io.ReadAll(io.LimitReader(resp.Body, maxUnofferedToolErrorBody+1))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(peek), resp.Body), resp.Body}
	var decoded struct {
		Error string `json:"error"`
	}
	if len(peek) > maxUnofferedToolErrorBody || json.Unmarshal(peek, &decoded) != nil {
		return resp, nil
	}
	if _, ok := unofferedToolCall(decoded.Error, offered); ok {
		resp.StatusCode, resp.Status = http.StatusOK, "200 OK"
	}
	return resp, nil
}
