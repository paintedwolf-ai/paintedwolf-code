package modelcall

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

type RequestControlCapture struct {
	mu       sync.Mutex
	requests []map[string]json.RawMessage
	// Wire receives serialized adapter requests only for an opted-in collection.
	Wire *WireCapture
}

// RecordDispatch records an adapter dispatch together with the request the
// adapter serialized, so an opted-in collection can render that request again.
func (c *RequestControlCapture) RecordDispatch(req CompletionRequest, body []byte) {
	if c == nil {
		return
	}
	c.Wire.record(&req, body)
	c.recordControls(body)
}

func (c *RequestControlCapture) Record(body []byte) {
	if c == nil {
		return
	}
	c.Wire.Record(body)
	c.recordControls(body)
}

func (c *RequestControlCapture) recordControls(body []byte) {
	var wire map[string]json.RawMessage
	if json.Unmarshal(body, &wire) != nil {
		return
	}
	controls := map[string]json.RawMessage{}
	for _, key := range []string{"reasoning_effort", "thinking", "reasoning", "max_tokens", "max_completion_tokens", "temperature", "output_config", "think"} {
		if value, ok := wire[key]; ok {
			controls[key] = value
		}
	}
	for key, names := range map[string][]string{
		"options":          {"num_ctx", "num_predict", "temperature"},
		"generationConfig": {"maxOutputTokens", "temperature", "thinkingConfig"},
	} {
		if raw, ok := wire[key]; ok {
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) != nil {
				continue
			}
			selected := map[string]json.RawMessage{}
			for _, name := range names {
				if value, ok := nested[name]; ok {
					selected[name] = value
				}
			}
			if encoded, err := json.Marshal(selected); err == nil {
				controls[key] = encoded
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, controls)
}

// RequestRenderer serializes a request exactly as an adapter would dispatch it,
// without contacting the provider. Adapters that implement it can be the
// serving target of a training collection.
type RequestRenderer interface {
	RenderRequest(ctx context.Context, req CompletionRequest) ([]byte, error)
}

func (c *RequestControlCapture) Snapshot() []map[string]json.RawMessage {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]json.RawMessage(nil), c.requests...)
}

// SourceRequestCapture retains only the latest outbound provider request.
type SourceRequestCapture struct {
	mu       sync.Mutex
	messages []api.Message
}

func CaptureSourceRequest(req CompletionRequest) (CompletionRequest, *SourceRequestCapture) {
	capture := &SourceRequestCapture{}
	req.sourceCapture = capture
	return req, capture
}

func (req CompletionRequest) CaptureSources() {
	if req.sourceCapture == nil {
		return
	}
	req.sourceCapture.mu.Lock()
	defer req.sourceCapture.mu.Unlock()
	req.sourceCapture.messages = append([]api.Message(nil), req.Messages...)
}

func (capture *SourceRequestCapture) ForCompletion(completion *Completion) *api.SourceContext {
	capture.mu.Lock()
	messages := capture.messages
	capture.mu.Unlock()
	var represented strings.Builder
	represented.WriteString(completion.Content)
	for _, call := range completion.ToolCalls {
		args, err := json.Marshal(call.Args)
		if err != nil {
			continue
		}
		represented.WriteByte('\n')
		represented.Write(args)
	}
	return sourceref.ForResponse(messages, represented.String())
}
