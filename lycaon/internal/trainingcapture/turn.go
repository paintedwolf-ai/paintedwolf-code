// Package trainingcapture writes opt-in collection evidence, never acceptance
// or quality labels. The collector joins these records to settled host receipts.
package trainingcapture

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

type contextKey struct{}

type Turn struct {
	root   string
	record Record
	wire   *modelcall.WireCapture
}

type Record struct {
	Schema     string          `json:"schema"`
	Epoch      string          `json:"epoch"`
	SessionID  string          `json:"session_id"`
	MessageID  string          `json:"message_id"`
	OutputID   string          `json:"output_id"`
	CallID     string          `json:"call_id"`
	Surface    string          `json:"surface"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	Dispatches int             `json:"dispatches"`
	Settled    bool            `json:"settled"`
	Reason     string          `json:"exclusion_reason,omitempty"`
	Request    json.RawMessage `json:"request,omitempty"`
	Response   json.RawMessage `json:"response,omitempty"`
}

// Begin is disabled unless an absolute private capture directory and epoch are
// explicitly configured. It does not enable debug logs or provider credentials.
func Begin(ctx context.Context, session, message, output string) (context.Context, *Turn) {
	root, epoch := os.Getenv("PW_TRAINING_CAPTURE_DIR"), os.Getenv("PW_TRAINING_EPOCH")
	if !filepath.IsAbs(root) || epoch == "" {
		return ctx, nil
	}
	t := &Turn{root: root, wire: &modelcall.WireCapture{}, record: Record{
		Schema: "pw-training-turn/1", Epoch: epoch, SessionID: session,
		MessageID: message, OutputID: output,
	}}
	return context.WithValue(ctx, contextKey{}, t), t
}

func Prepare(ctx context.Context, req modelcall.CompletionRequest) modelcall.CompletionRequest {
	t, _ := ctx.Value(contextKey{}).(*Turn)
	if t == nil {
		return req
	}
	t.record.CallID, t.record.Surface = req.Debug.CallID, req.Debug.Surface
	req.ControlCapture = &modelcall.RequestControlCapture{Wire: t.wire}
	return req
}

// Observe snapshots the provider response before host normalization. A failed,
// scripted, or reasoning-bearing response cannot become a non-thinking target.
func Observe(ctx context.Context, completion *modelcall.Completion, failed bool) {
	t, _ := ctx.Value(contextKey{}).(*Turn)
	if t == nil {
		return
	}
	if failed || completion == nil {
		t.record.Reason = "provider_failed"
		return
	}
	if completion.Scripted || completion.Reasoning != "" || len(completion.ReasoningDetails) > 0 {
		t.record.Reason = "unsupported_response"
		return
	}
	t.record.Provider, t.record.Model = completion.ProviderID, completion.Model
	if completion.ToolCalls == nil {
		copy := *completion
		copy.ToolCalls = []api.ToolCall{}
		completion = &copy
	}
	t.record.Response, _ = json.Marshal(struct {
		Content string `json:"content"`
		Calls   any    `json:"tool_calls"`
	}{completion.Content, completion.ToolCalls})
}

func Settled(ctx context.Context, output string, message api.Message) {
	t, _ := ctx.Value(contextKey{}).(*Turn)
	if t == nil {
		return
	}
	t.record.OutputID, t.record.Settled = output, output != ""
	normalized, err := json.Marshal(struct {
		Content string `json:"content"`
		Calls   any    `json:"tool_calls"`
	}{message.Content, message.ToolCalls})
	if err != nil || !bytes.Equal(normalized, t.record.Response) {
		t.record.Reason = "host_modified_response"
	}
}
