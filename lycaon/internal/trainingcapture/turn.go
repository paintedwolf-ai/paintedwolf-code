// Package trainingcapture writes opt-in collection evidence, never acceptance
// or quality labels. The collector joins these records to settled host receipts.
//
// A training row pairs the request a student will be served with the response a
// student should emit. The capture therefore records the exact teacher dispatch
// as provenance, and renders the same screened request through the student's
// configured serving provider, which is the training input.
package trainingcapture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

type contextKey struct{}

// Policy is what the host supplies to a collection turn.
type Policy struct {
	// StorageChanges reports whether the durable transcript policy would change
	// a message; content it would not persist never enters a training artifact.
	StorageChanges func(api.Message) bool
	// Render serializes a request as the configured provider would dispatch it
	// to model, returning the provider's kind.
	Render func(ctx context.Context, provider, model string, req modelcall.CompletionRequest) (string, []byte, error)
}

type Turn struct {
	root    string
	student student
	record  Record
	wire    *modelcall.WireCapture
	raw     *response
	policy  Policy
}

// student is the configured provider and model the student will be served through.
type student struct{ provider, model string }

type Record struct {
	Schema     string `json:"schema"`
	Epoch      string `json:"epoch"`
	SessionID  string `json:"session_id"`
	MessageID  string `json:"message_id"`
	OutputID   string `json:"output_id"`
	CallID     string `json:"call_id"`
	Surface    string `json:"surface"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Dispatches int    `json:"dispatches"`
	Settled    bool   `json:"settled"`
	Reason     string `json:"exclusion_reason,omitempty"`
	// Normalized names host changes the target absorbs; ModifiedFields names the
	// emitted fields the host changed, which exclude the turn. Both are field
	// names only, kept on exclusions so a collection can see why rows were lost.
	Normalized     []string `json:"normalized,omitempty"`
	ModifiedFields []string `json:"modified_fields,omitempty"`
	// PolicyLocation is the request message the durable transcript policy would change.
	PolicyLocation  string          `json:"policy_location,omitempty"`
	StudentProvider string          `json:"student_provider"`
	StudentKind     string          `json:"student_kind,omitempty"`
	StudentModel    string          `json:"student_model"`
	Request         json.RawMessage `json:"request,omitempty"`
	StudentRequest  json.RawMessage `json:"student_request,omitempty"`
	Response        json.RawMessage `json:"response,omitempty"`
}

// response is what a student emits for one turn: text and tool calls.
type response struct {
	Content string         `json:"content"`
	Calls   []api.ToolCall `json:"tool_calls"`
}

// config is the opt-in collection: a private absolute directory, an epoch, and
// the configured provider and model the student will be served through.
func config() (root, epoch string, s student, ok bool) {
	root, epoch = os.Getenv("PW_TRAINING_CAPTURE_DIR"), os.Getenv("PW_TRAINING_EPOCH")
	s = student{provider: os.Getenv("PW_TRAINING_STUDENT_PROVIDER"), model: os.Getenv("PW_TRAINING_STUDENT_MODEL")}
	return root, epoch, s, filepath.IsAbs(root) && epoch != "" && s.provider != "" && s.model != ""
}

// Begin is disabled unless the collection is fully configured. It does not
// enable debug logs or provider credentials.
func Begin(ctx context.Context, session, message, output string) (context.Context, *Turn) {
	root, epoch, s, ok := config()
	if !ok {
		return ctx, nil
	}
	t := &Turn{root: root, student: s, wire: &modelcall.WireCapture{}, record: Record{
		Schema: "pw-training-turn/1", Epoch: epoch, SessionID: session,
		MessageID: message, OutputID: output, StudentProvider: s.provider, StudentModel: s.model,
	}}
	return context.WithValue(ctx, contextKey{}, t), t
}

func Prepare(ctx context.Context, req modelcall.CompletionRequest, policy Policy) modelcall.CompletionRequest {
	t, _ := ctx.Value(contextKey{}).(*Turn)
	if t == nil {
		return req
	}
	t.record.CallID, t.record.Surface = req.Debug.CallID, req.Debug.Surface
	t.policy = policy
	req.ControlCapture = &modelcall.RequestControlCapture{Wire: t.wire}
	return req
}

// Observe snapshots the provider response before host normalization. A failed,
// scripted, or reasoning-bearing response cannot become a non-thinking target.
// The provider and model are the ones the call was dispatched to; a collected
// stream does not always carry them on the completion.
func Observe(ctx context.Context, completion *modelcall.Completion, failed bool, provider, model string) {
	t, _ := ctx.Value(contextKey{}).(*Turn)
	if t == nil {
		return
	}
	t.record.Provider, t.record.Model = provider, model
	if failed || completion == nil {
		t.record.Reason = "provider_failed"
		return
	}
	if completion.Scripted || completion.Reasoning != "" || len(completion.ReasoningDetails) > 0 {
		t.record.Reason = "unsupported_response"
		return
	}
	raw := response{Content: completion.Content, Calls: append([]api.ToolCall{}, completion.ToolCalls...)}
	t.raw = &raw
}

// Settled records the training input and target once the host settled the
// output. The target is the settled message when the host changed only what a
// student never emits (call identifiers, duplicate or argument-less sibling
// calls it removed): the receipts that admit the turn name the settled calls.
// The settled message is already storage-safe, so a durable-policy change to the
// response surfaces here as modified content.
func Settled(ctx context.Context, output string, message api.Message) {
	t, _ := ctx.Value(contextKey{}).(*Turn)
	if t == nil {
		return
	}
	t.record.OutputID, t.record.Settled = output, output != ""
	if t.record.Reason != "" {
		return
	}
	if t.raw == nil {
		t.record.Reason = "missing_response"
		return
	}
	settled := response{Content: message.Content, Calls: append([]api.ToolCall{}, message.ToolCalls...)}
	t.record.Normalized, t.record.ModifiedFields = compareResponses(*t.raw, settled)
	if len(t.record.ModifiedFields) > 0 {
		t.record.Reason = "host_modified_response"
		return
	}
	_, source, count := t.wire.Snapshot()
	if count != 1 || source == nil {
		return // Finish names the dispatch fault.
	}
	if t.policy.StorageChanges == nil || t.policy.Render == nil {
		t.record.Reason = "collection_policy_unavailable"
		return
	}
	for i, msg := range source.Messages {
		if t.policy.StorageChanges(msg) {
			t.record.Reason, t.record.PolicyLocation = "transcript_policy_changes_request", fmt.Sprintf("messages[%d]", i)
			return
		}
	}
	// A request the student could not be served (an unconfigured provider, or one
	// over its declared window) is not a training input for it.
	kind, rendered, err := t.policy.Render(ctx, t.student.provider, t.student.model, *source)
	if err != nil {
		t.record.Reason = "student_cannot_be_served"
		return
	}
	t.record.StudentKind, t.record.StudentRequest = kind, rendered
	t.record.Response, _ = json.Marshal(settled)
}

// compareResponses separates host normalizations from changes to emitted text,
// tool names, or arguments. Settled calls must appear, in order, among the raw
// calls; presentation fields the host adds are not part of what a student emits.
func compareResponses(raw, settled response) (normalized, modified []string) {
	if raw.Content != settled.Content {
		modified = append(modified, "content")
	}
	next, idsChanged := 0, false
	for _, call := range settled.Calls {
		key := callKey(call)
		for next < len(raw.Calls) && callKey(raw.Calls[next]) != key {
			next++
		}
		if next == len(raw.Calls) {
			return normalized, append(modified, "tool_calls")
		}
		idsChanged = idsChanged || raw.Calls[next].ID != call.ID
		next++
	}
	if len(settled.Calls) < len(raw.Calls) {
		normalized = append(normalized, "dropped_calls")
	}
	if idsChanged {
		normalized = append(normalized, "call_ids")
	}
	return normalized, modified
}

func callKey(call api.ToolCall) string {
	args, err := json.Marshal(call.Args)
	if err != nil {
		args = []byte(fmt.Sprintf("unencodable:%p", &call))
	}
	return fmt.Sprintf("%s\x00%s\x00%t\x00%t", call.Name, args, call.ArgsTruncated, call.ArgsMalformed)
}
