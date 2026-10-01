package observability

import (
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	sseDebugMaxEnv     = "LYCAON_SSE_DEBUG_MAX_BYTES"
	defaultSSEMaxBytes = 32 * 1024
)

// SSEDebugEnabled reports whether SSE publish payloads are mirrored to a debug log file.
func SSEDebugEnabled() bool {
	return debugpaths.Enabled(debugpaths.KindSSE)
}

type sseDebugEntry struct {
	Time         time.Time         `json:"ts"`
	Topic        api.EventTopic    `json:"topic"`
	ProjectID    string            `json:"project_id,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	Envelope     api.EventEnvelope `json:"envelope"`
	DataBytes    int64             `json:"data_bytes,omitempty"`
	DataWithheld bool              `json:"data_withheld,omitempty"`
}

var (
	sseDebugOnce sync.Once
	sseDebug     *jsonlDebugLog
)

func initSSEDebugLog() {
	if !SSEDebugEnabled() {
		return
	}
	log, err := openJSONLDebugLog(true, debugpaths.KindSSE)
	if err != nil {
		slog.Warn("sse debug logging disabled", "err", err)
		return
	}
	sseDebug = log
	slog.Info("sse debug logging enabled", "path", log.path)
}

func activeSSEDebugLog() *jsonlDebugLog {
	sseDebugOnce.Do(initSSEDebugLog)
	return sseDebug
}

// LogSSEPublish records an EventHub publish when LYCAON_SSE_DEBUG is enabled.
func LogSSEPublish(topic api.EventTopic, projectID, sessionID string, envelope api.EventEnvelope) {
	log := activeSSEDebugLog()
	if log == nil {
		return
	}
	dataBytes := len(envelope.Data)
	data, withheld := sseDebugEnvelopeData(envelope.Data)
	if withheld && topic == api.EventTopicSourceChanged {
		// A source batch too large to mirror is exactly the one a forensic
		// read needs; keep its shape instead of a byte count.
		if summary, ok := sseSourceChangesSummary(envelope.Data); ok {
			data = summary
		}
	}
	envelope.Data = data
	entry := sseDebugEntry{
		Time:         time.Now().UTC(),
		Topic:        topic,
		ProjectID:    projectID,
		SessionID:    sessionID,
		Envelope:     redactSSEEnvelope(envelope),
		DataBytes:    int64(dataBytes),
		DataWithheld: withheld,
	}
	log.write(entry)
}

func redactSSEEnvelope(env api.EventEnvelope) api.EventEnvelope {
	return api.EventEnvelope{
		V:           env.V,
		Topic:       env.Topic,
		PublishedAt: env.PublishedAt,
		Scope:       env.Scope,
		Data:        env.Data,
	}
}

// sseSourceChangesSummary reduces an oversized source batch to counts: how
// many changes, whether it asked for a resync, and how they spread across
// top-level directories and operations.
func sseSourceChangesSummary(data json.RawMessage) (json.RawMessage, bool) {
	var ev api.SourceChangesEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil, false
	}
	byTopLevel := make(map[string]int)
	byOp := make(map[string]int)
	for _, change := range ev.Changes {
		top := change.Path
		if i := strings.IndexByte(top, '/'); i >= 0 {
			top = top[:i]
		}
		byTopLevel[top]++
		byOp[string(change.Op)]++
	}
	body, err := json.Marshal(struct {
		Capture       string         `json:"capture"`
		OriginalBytes int            `json:"original_bytes"`
		ProjectID     string         `json:"project_id"`
		WorkspaceID   string         `json:"workspace_id"`
		Changes       int            `json:"changes"`
		Resync        bool           `json:"resync"`
		GitChanged    bool           `json:"git_changed"`
		ByTopLevel    map[string]int `json:"by_top_level"`
		ByOp          map[string]int `json:"by_op"`
	}{
		Capture: "summarized", OriginalBytes: len(data),
		ProjectID: ev.ProjectID, WorkspaceID: ev.WorkspaceID,
		Changes: len(ev.Changes), Resync: ev.Resync, GitChanged: ev.GitChanged,
		ByTopLevel: byTopLevel, ByOp: byOp,
	})
	if err != nil {
		return nil, false
	}
	return body, true
}

// sseDebugEnvelopeData returns JSON-safe envelope data for debug JSONL (never empty RawMessage).
func sseDebugEnvelopeData(data json.RawMessage) (json.RawMessage, bool) {
	if len(data) == 0 {
		return nil, false
	}
	if len(data) > sseDebugMaxBytes() {
		body, err := json.Marshal(struct {
			Capture       string `json:"capture"`
			OriginalBytes int    `json:"original_bytes"`
		}{
			Capture:       "withheld",
			OriginalBytes: len(data),
		})
		if err != nil {
			return nil, true
		}
		return body, true
	}
	redacted := RedactCaptureText(string(data))
	if len(redacted) == 0 {
		// json.Marshal of a string does not fail; the error branch is unreachable.
		b, err := json.Marshal(string(data))
		if err != nil {
			return nil, false
		}
		return json.RawMessage(b), false
	}
	if json.Valid([]byte(redacted)) {
		return json.RawMessage(redacted), false
	}
	b, err := json.Marshal(redacted)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(b), false
}

func sseDebugMaxBytes() int {
	raw := strings.TrimSpace(os.Getenv(sseDebugMaxEnv))
	if raw == "" {
		return defaultSSEMaxBytes
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultSSEMaxBytes
	}
	return n
}
