package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// The JSON error body carries one short parser error.
const maxToolCallErrorBody = 4 << 10

// toolCallRejectable reports whether a parser error can only come from a
// dropped call: the request offered callable tools (a forbidden request sends
// none) and does not constrain output with a format.
func (r Request) toolCallRejectable() bool {
	return len(r.Tools) > 0 && len(r.Format) == 0
}

// providerError types an Ollama error message through the kind's declared
// tool-call rejection grammars. The message is never parsed into a call.
func (r Request) providerError(rejections providerprofile.ToolCallRejectionPolicy, providerID, message string) error {
	return rejections.Classify(providerID, r.Model, message, r.toolCallRejectable(), fmt.Errorf("ollama: %s", message))
}

// admitToolCallRejection reads a declared rejection sent before any output as
// the in-band error line it is after output: Ollama's streamResponse and
// waitForStream (server/routes.go) pick the error status only because nothing
// had been written yet.
func (r Request) admitToolCallRejection(rejections providerprofile.ToolCallRejectionPolicy, resp *http.Response, err error) (*http.Response, error) {
	if err != nil || resp == nil || rejections.StatusBeforeOutput == 0 || resp.StatusCode != rejections.StatusBeforeOutput || !r.toolCallRejectable() {
		return resp, err
	}
	peek, _ := io.ReadAll(io.LimitReader(resp.Body, maxToolCallErrorBody+1))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(peek), resp.Body), resp.Body}
	var decoded struct {
		Error string `json:"error"`
	}
	if len(peek) > maxToolCallErrorBody || json.Unmarshal(peek, &decoded) != nil {
		return resp, nil
	}
	if _, ok := rejections.Match(decoded.Error); ok {
		resp.StatusCode, resp.Status = http.StatusOK, "200 OK"
	}
	return resp, nil
}
