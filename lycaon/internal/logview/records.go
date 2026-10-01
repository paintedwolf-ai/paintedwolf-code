// Package logview reads the JSONL capture files written by a full-debug sidecar
// session (LLM payloads, HTTP requests, SSE events, session topology, Den stalls)
// and renders them in a human-friendly form for local debugging.
//
// Captures live under ~/.config/paintedwolf/debug/sessions/<timestamp>/, one
// directory per engine run; see docs/dev-tasks.md.
package logview

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// LLMRecord is one provider call captured in llm-requests.jsonl.
type LLMRecord struct {
	TS            time.Time    `json:"ts"`
	Call          string       `json:"call"`
	Surface       string       `json:"surface"`
	AgentType     string       `json:"agent_type"`
	Model         string       `json:"model"`
	ProviderID    string       `json:"provider_id"`
	ProfileID     string       `json:"profile_id"`
	SessionID     string       `json:"session_id"`
	Iteration     int          `json:"iteration"`
	MaxIterations int          `json:"max_iterations"`
	DurationMS    *int         `json:"duration_ms"`
	ToolNames     []string     `json:"tool_names"`
	Messages      []LLMMessage `json:"messages"`
	Usage         *LLMUsage    `json:"usage"`
}

// LLMMessage is one entry in a call's transcript. Content is captured as either a
// JSON string or a structured content-block array depending on the provider.
type LLMMessage struct {
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []LLMToolCall   `json:"tool_calls"`
	TS        time.Time       `json:"ts"`
}

// LLMToolCall is a model-requested tool invocation attached to an assistant message.
type LLMToolCall struct {
	Name string          `json:"name"`
	ID   string          `json:"id"`
	Args json.RawMessage `json:"args"`
}

// LLMUsage is the token accounting for a call.
type LLMUsage struct {
	PromptTokens             int `json:"prompt_tokens"`
	CompletionTokens         int `json:"completion_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// HTTPRecord is one request captured in http-requests.jsonl.
type HTTPRecord struct {
	TS         time.Time `json:"ts"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	DurationMS *int      `json:"duration_ms"`
	RequestID  string    `json:"request_id"`
}

// SSERecord is one broadcast event captured in sse-events.jsonl.
type SSERecord struct {
	TS        time.Time   `json:"ts"`
	Topic     string      `json:"topic"`
	SessionID string      `json:"session_id"`
	ProjectID string      `json:"project_id"`
	Envelope  SSEEnvelope `json:"envelope"`
}

// SSEEnvelope is the wire envelope carried by an SSE event.
type SSEEnvelope struct {
	Topic string          `json:"topic"`
	V     int             `json:"v"`
	Data  json.RawMessage `json:"data"`
}

// Op extracts the mutation op (append/patch) from a message-topic envelope,
// or empty string when the payload carries none.
func (e SSEEnvelope) Op() string {
	if len(e.Data) == 0 {
		return ""
	}
	var probe struct {
		Op string `json:"op"`
	}
	_ = json.Unmarshal(e.Data, &probe)
	return probe.Op
}

// SessionRecord is one row of sessions.jsonl describing a created session and its
// place in the coordinator/worker topology.
type SessionRecord struct {
	TS              time.Time `json:"ts"`
	SessionID       string    `json:"session_id"`
	ParentSessionID string    `json:"parent_session_id"`
	AgentType       string    `json:"agent_type"`
	Surface         string    `json:"surface"`
	ProfileID       string    `json:"profile_id"`
	Task            string    `json:"task"`
}

// DenPerfRecord is one Den main-thread perf/stall line from den-perf.jsonl.
type DenPerfRecord struct {
	TS      time.Time      `json:"ts"`
	Channel string         `json:"channel,omitempty"`
	Event   string         `json:"event"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// IsStall reports events that block the Den main thread long enough to care.
func (r DenPerfRecord) IsStall() bool {
	switch r.Event {
	case "loop-stall", "longtask":
		return true
	default:
		return false
	}
}

// StallMS returns lag_ms (loop-stall) or dur_ms (longtask), whichever is set.
func (r DenPerfRecord) StallMS() int {
	if v := detailInt(r.Detail, "lag_ms"); v > 0 {
		return v
	}
	return detailInt(r.Detail, "dur_ms")
}

// Recent returns the breadcrumb trail Den attached to the stall, if any.
func (r DenPerfRecord) Recent() string {
	if r.Detail == nil {
		return ""
	}
	if s, ok := r.Detail["recent"].(string); ok {
		return s
	}
	return ""
}

func detailInt(detail map[string]any, key string) int {
	if detail == nil {
		return 0
	}
	v, ok := detail[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

// decodeJSONL streams a JSONL file, appending one decoded T per non-empty line.
// Malformed lines are skipped rather than aborting the whole view — a half-written
// trailing line from a live session must not blank the report.
func decodeJSONL[T any](path string) ([]T, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var out []T
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec T
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	if err := scan.Err(); err != nil {
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// ReadJSONL decodes a JSONL stream (file, stdin) into typed records, skipping
// malformed lines. It is the pipe-in entry point for the CLI.
func ReadJSONL[T any](r io.Reader) ([]T, error) {
	return decodeJSONLReader[T](r)
}

// ReadJSONLFile decodes a JSONL capture file, returning no records (and no error)
// when the file does not yet exist.
func ReadJSONLFile[T any](path string) ([]T, error) {
	recs, err := decodeJSONL[T](path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return recs, err
}

// decodeJSONLReader is the io.Reader form used by tests with in-memory fixtures.
func decodeJSONLReader[T any](r io.Reader) ([]T, error) {
	var out []T
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec T
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, scan.Err()
}
