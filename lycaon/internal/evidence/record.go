package evidence

// LineRange is an inclusive 1-based line interval observed by a read tool.
type LineRange struct {
	Start int
	End   int
}

// Record is one runtime evidence row: tool observation (Handle set) or gate row (GateType set).
type Record struct {
	RecordID string `json:"record_id,omitempty"`
	Handle   string // kind#ordinal, e.g. read#3
	Kind     string
	Shape    string // evidence shape id (file_region, url, …)
	// Fidelity describes capture quality, independent of content trust.
	Fidelity   string
	SourceTool string // registry tool name that produced this record
	// SupersededBy changes path lookup currency, never exact handle identity.
	SupersededBy string `json:"-"`
	Survey       bool   // true for survey-grade observations
	Truncated    bool   // body capture exceeded EvidenceCaptureBodyCapBytes
	Path         string // normalized repo-relative when the tool touched a path
	LineRanges   []LineRange
	Body         []string // result lines for excerpt verification
	URL          string   // primary observed URL

	Surface    string // dom | tui | http
	ArtifactID string
	FrameIndex int // positive values identify temporal frames

	grepLines    map[string]map[int]string // grep kind: path → line → content
	pathsTouched []string                  // additional normalized paths for ByPath index
	pathTiers    map[string]string         // normalized path → trust tier at capture
	urlsTouched  []string                  // every URL named by the result
	urlTitles    map[string]string         // URL → page title the tool reported

	// Gate/inspector fields — populated when GateType != "" (scan verify/security rows).
	GateType       string         `json:"type,omitempty"`
	Slot           string         `json:"slot,omitempty"`
	RunID          string         `json:"run_id,omitempty"`
	GateVerdict    string         `json:"verdict,omitempty"`
	Summary        string         `json:"summary,omitempty"`
	Artifacts      map[string]any `json:"artifacts,omitempty"`
	InspectorAgent string         `json:"inspector_agent,omitempty"`
	GateModel      string         `json:"model,omitempty"`
	Attempt        int            `json:"attempt,omitempty"`
	HeadSHA        string         `json:"head_sha,omitempty"`
	RecordedAt     string         `json:"at,omitempty"` // RFC3339 in gate JSONL
}

// IsGate reports whether rec is an inspector gate row.
func (r Record) IsGate() bool { return r.GateType != "" }

// Ledger is the handle-keyed evidence ledger for citation grounding.
type Ledger struct {
	Handles      map[string]Record   // handle → record (single store)
	ByPath       map[string][]string // normalized path → handles (newest last)
	PathFidelity map[string]string   // normalized path → trust tier (structured wins)
	handleOrder  []string            // observation order for stable HandlesSorted
}

// OffenderReport is the bounded reject display for hint templates.
type OffenderReport struct {
	Count   int
	Sample  string
	Omitted int
}
