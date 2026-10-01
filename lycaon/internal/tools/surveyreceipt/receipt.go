package surveyreceipt

import (
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// Receipt records read-only survey coverage.
type Receipt struct {
	Tool          string         `json:"tool"`
	Path          string         `json:"path,omitempty"`
	PathsTouched  int            `json:"paths_touched"`
	BytesReturned int            `json:"bytes_returned"`
	Truncated     bool           `json:"truncated"`
	ScopeHash     string         `json:"scope_hash"`
	Age           *AgeContext    `json:"age,omitempty"`
	References    *RefContext    `json:"references,omitempty"`
	Source        *SourceContext `json:"source,omitempty"`
}

// SourceContext identifies served bytes in the source ledger.
type SourceContext struct {
	// Navigation is a root-addressed Markdown destination for this location.
	Navigation string `json:"navigation,omitempty"`
	// SHA256Short identifies the served bytes (first 12 hex of their SHA-256).
	SHA256Short string `json:"sha256_12"`
	// Recorded is true when the ledger's tracked head is exactly these bytes.
	Recorded bool `json:"recorded"`
	// VersionID addresses the recorded state (source_history, versions, Walk).
	VersionID string `json:"version_id,omitempty"`
	// LastChange is the newest recorded effect on this file.
	LastChange *SourceChange `json:"last_change,omitempty"`
	// Note explains a Recorded false: what the ledger could not place.
	Note string `json:"note,omitempty"`
	// Editor identifies reads from an open editor document.
	Editor *EditorContext `json:"editor,omitempty"`
}

// EditorContext distinguishes unsaved edits from changes to the backing file.
type EditorContext struct {
	Revision int64 `json:"revision"`
	Dirty    bool  `json:"dirty"`
	Diverged bool  `json:"diverged,omitempty"`
	// Absent says the document served text for a path that has no file.
	Absent bool `json:"absent,omitempty"`
}

// SourceChange identifies an effect's actor relative to the reading session.
type SourceChange struct {
	Actor  string `json:"actor"`
	Detail string `json:"detail,omitempty"`
	Op     string `json:"op"`
	At     string `json:"at"`
	Turn   int    `json:"turn,omitempty"`
}

// RefContext compares resolvable file references by history and session read state.
type RefContext struct {
	Named       []Ref  `json:"named"`
	NewerCount  int    `json:"newer_count"`
	UnreadCount int    `json:"unread_count,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Summary     string `json:"summary"`
}

// Ref describes a referenced file and its session read state.
type Ref struct {
	Path        string `json:"path"`
	LastChanged string `json:"last_changed,omitempty"`
	Newer       bool   `json:"newer,omitempty"`
	Read        *bool  `json:"read,omitempty"`
}

// AgeContext locates a tracked file in repository history.
type AgeContext struct {
	LastChanged  string `json:"last_changed"`
	OlderThanPct int    `json:"older_than_pct"`
	TrackedFiles int    `json:"tracked_files"`
	Summary      string `json:"summary"`
}

const openScopeHash = "open"

// New builds a receipt for a successful survey tool invocation.
func New(tool, path string, pathsTouched, bytesReturned int, truncated bool) Receipt {
	return Receipt{
		Tool:          strings.TrimSpace(tool),
		Path:          strings.TrimSpace(path),
		PathsTouched:  pathsTouched,
		BytesReturned: bytesReturned,
		Truncated:     truncated,
		ScopeHash:     openScopeHash,
	}
}

// Attach wraps tool output with a JSON envelope containing a receipt.
func Attach(toolOutput string, r Receipt) string {
	toolOutput = strings.TrimSpace(toolOutput)
	if toolOutput == "" {
		out, _ := surveyjson.Marshal(map[string]any{"receipt": r})
		return string(out)
	}
	var parsed any
	if err := json.Unmarshal([]byte(toolOutput), &parsed); err == nil {
		switch v := parsed.(type) {
		case map[string]any:
			v["receipt"] = r
			out, _ := surveyjson.Marshal(v)
			return string(out)
		case []any:
			out, _ := surveyjson.Marshal(map[string]any{"matches": v, "receipt": r})
			return string(out)
		}
	}
	out, _ := surveyjson.Marshal(map[string]any{"content": toolOutput, "receipt": r})
	return string(out)
}

// Parse extracts a receipt from tool JSON output when present.
func Parse(toolOutput string) (Receipt, bool) {
	toolOutput = strings.TrimSpace(toolOutput)
	if toolOutput == "" {
		return Receipt{}, false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(toolOutput), &payload); err != nil {
		return Receipt{}, false
	}
	raw, ok := payload["receipt"]
	if !ok {
		return Receipt{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return Receipt{}, false
	}
	var r Receipt
	if err := json.Unmarshal(b, &r); err != nil {
		return Receipt{}, false
	}
	if strings.TrimSpace(r.Tool) == "" {
		return Receipt{}, false
	}
	return r, true
}
