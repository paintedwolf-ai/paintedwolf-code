package ollama

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
)

// Ollama's tool-call parsers resolve the offered tool and then json.Unmarshal
// its arguments (ollama/ollama model/parsers/ministral.go Add). A failure drops
// the call and reports only Go's encoding/json SyntaxError text, which names
// neither the tool nor the arguments. The grammar is that error's exact shape:
// "unexpected end of JSON input", or "invalid character " + quoteChar(c) + " "
// + a scanner context (encoding/json scanner.go).
var toolCallSyntaxError = regexp.MustCompile(`^(?:unexpected end of JSON input|invalid character '(?:[^'\\]|\\['\\abfnrtv]|\\x[0-9a-f]{2}|\\u[0-9a-f]{4})' (?:` +
	strings.Join(quoteAll(
		"looking for beginning of value",
		"looking for beginning of object key string",
		"after object key",
		"after object key:value pair",
		"after array element",
		"after top-level value",
		"in string literal",
		"in string escape code",
		`in \u hexadecimal character escape`,
		"in numeric literal",
		"after decimal point in numeric literal",
		"in exponent of numeric literal",
	), "|") + `|in literal (?:true|false|null) \(expecting '[a-z]'\)))$`)

// The JSON error body carries one short syntax error.
const maxToolCallErrorBody = 4 << 10

func quoteAll(phrases ...string) []string {
	for i, phrase := range phrases {
		phrases[i] = regexp.QuoteMeta(phrase)
	}
	return phrases
}

// toolCallRecoverable reports whether a parser rejection may stay inside the
// turn: the request offered callable tools and does not constrain output with
// a format, so a JSON syntax error can only come from a dropped tool call.
func (r Request) toolCallRecoverable() bool {
	return len(r.Tools) > 0 && len(r.Format) == 0
}

// providerError types an Ollama error message. A recoverable request's tool
// arguments syntax error becomes ProviderToolCallUnparsedError; the message
// is never parsed into a call.
func (r Request) providerError(providerID, message string) error {
	cause := fmt.Errorf("ollama: %s", message)
	if !r.toolCallRecoverable() || !toolCallSyntaxError.MatchString(message) {
		return cause
	}
	return &failure.ProviderToolCallUnparsedError{ProviderID: providerID, Model: r.Model, Cause: cause}
}

// admitToolCallSyntaxError reads a recoverable rejection sent before any
// output as the in-band error line it is after output: Ollama's streamResponse
// and waitForStream (server/routes.go) pick HTTP 500 only because nothing had
// been written yet.
func (r Request) admitToolCallSyntaxError(resp *http.Response, err error) (*http.Response, error) {
	if err != nil || resp == nil || resp.StatusCode != http.StatusInternalServerError || !r.toolCallRecoverable() {
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
	if toolCallSyntaxError.MatchString(decoded.Error) {
		resp.StatusCode, resp.Status = http.StatusOK, "200 OK"
	}
	return resp, nil
}
