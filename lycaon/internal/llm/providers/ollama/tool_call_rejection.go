package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

// The JSON error body carries one short parser error.
const maxToolCallErrorBody = 4 << 10

// rejectableTools is the offered set a parser rejection is checked against:
// empty when no callable tools were sent (a forbidden request sends none) or a
// format constrains output, so every error stays a failure.
func (r Request) rejectableTools() []string {
	if len(r.Format) > 0 {
		return nil
	}
	names := make([]string, 0, len(r.Tools))
	for _, tool := range r.Tools {
		names = append(names, tool.Function.Name)
	}
	return names
}

// toolCallRejection answers an Ollama error message through the kind's
// declared parser grammars. An unoffered name becomes a bare call outside the
// offered set, which the host refuses with TOOL_NOT_OFFERED before any lookup;
// a dropped call's syntax error becomes a typed failure. Everything else is
// the original error.
func (r Request) toolCallRejection(rejections providerprofile.ToolCallRejectionPolicy, providerID, message string) (api.ToolCall, bool, error) {
	offered := r.rejectableTools()
	if name, ok := rejections.UnofferedTool(message, offered); ok {
		return api.ToolCall{ID: providerwire.NewToolCallID(), Name: name, Args: map[string]any{}}, true, nil
	}
	return api.ToolCall{}, false, rejections.Classify(providerID, r.Model, message, len(offered) > 0, fmt.Errorf("ollama: %s", message))
}

// rejectionCompletion answers a non-stream error body: only a refused call
// becomes a completion.
func (r Request) rejectionCompletion(rejections providerprofile.ToolCallRejectionPolicy, providerID string, decoded ollamaChatResponse) (*modelcall.Completion, error) {
	tc, refused, err := r.toolCallRejection(rejections, providerID, decoded.Error)
	if !refused {
		return nil, err
	}
	return &modelcall.Completion{
		Content:   decoded.Message.Content,
		Reasoning: decoded.Message.Thinking,
		ToolCalls: append(ollamaToolCallsToAPI(decoded.Message.ToolCalls), tc),
	}, nil
}

// admitToolCallRejection reads a declared rejection sent before any output as
// the in-band error line it is after output: Ollama's streamResponse and
// waitForStream (server/routes.go) pick the error status only because nothing
// had been written yet.
func (r Request) admitToolCallRejection(rejections providerprofile.ToolCallRejectionPolicy, resp *http.Response, err error) (*http.Response, error) {
	offered := r.rejectableTools()
	if err != nil || resp == nil || rejections.StatusBeforeOutput == 0 || resp.StatusCode != rejections.StatusBeforeOutput || len(offered) == 0 {
		return resp, err
	}
	peek, _ := io.ReadAll(io.LimitReader(resp.Body, maxToolCallErrorBody+1))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(peek), resp.Body), resp.Body}
	if text, ok := boundedErrorText(peek); ok && rejections.Recognizes(text, offered) {
		resp.StatusCode, resp.Status = http.StatusOK, "200 OK"
	}
	return resp, nil
}

// boundedErrorText reads the error field of a whole JSON error body; a body
// past the bound or not JSON is passed through unchanged.
func boundedErrorText(peek []byte) (string, bool) {
	if len(peek) > maxToolCallErrorBody {
		return "", false
	}
	var decoded struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(peek, &decoded) != nil {
		return "", false
	}
	return decoded.Error, true
}
