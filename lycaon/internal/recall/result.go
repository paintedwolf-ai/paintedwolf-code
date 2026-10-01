package recall

import "github.com/lycaon/lycaon/pkg/api"

// Result is what one recall call states. An empty Hits list means whatever
// Resolution says it means.
type Result struct {
	Resolution Resolution `json:"resolution"`
	// ScopeStated echoes the reach the host actually applied.
	ScopeStated string `json:"scope_stated"`
	// QueryStated echoes the compiler's reading of the query.
	QueryStated string `json:"query_stated,omitempty"`
	Count       Count  `json:"count"`
	// Removed counts tombstoned rows the query touched: part of the answer is
	// gone rather than absent.
	Removed int   `json:"removed,omitempty"`
	Hits    []Hit `json:"hits,omitempty"`
	// Facets map the scope when a query is too broad to answer with bodies.
	Facets *Facets `json:"facets,omitempty"`
	// Truncated is set when the answer hit a display bound.
	Truncated bool `json:"truncated,omitempty"`
	// Issues names executor failures. Present only with a degraded resolution.
	Issues []string `json:"issues,omitempty"`
	// NextAction names a reachable exit when the answer was not what the caller
	// wanted.
	NextAction string  `json:"next_action,omitempty"`
	Receipt    Receipt `json:"receipt"`
}

// Count carries an explicit relation so a capped generation is not presented as
// an exact total.
type Count struct {
	Value    int    `json:"value"`
	Relation string `json:"relation"`
}

const (
	// RelationExact — Value is the whole answer.
	RelationExact = "exact"
	// RelationAtLeast — Value is a lower bound; the generation was capped.
	RelationAtLeast = "at_least"
)

// Hit is one recalled observation.
type Hit struct {
	SourceContext *api.SourceContext `json:"-"`
	// Handle addresses the observation in the producing session's ledger.
	Handle string `json:"handle,omitempty"`
	// HitID is stable row identity across calls.
	HitID string `json:"hit_id"`
	Kind  string `json:"kind"`
	Tool  string `json:"tool,omitempty"`
	// SessionID and AgentType attribute the row to the leg that observed it.
	SessionID string `json:"session_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	LegID     string `json:"leg_id,omitempty"`
	// Mine marks rows this caller observed itself.
	Mine     bool     `json:"mine,omitempty"`
	TS       string   `json:"observed_at,omitempty"`
	Path     string   `json:"path,omitempty"`
	Line     int      `json:"line,omitempty"`
	URL      string   `json:"url,omitempty"`
	Currency Currency `json:"currency,omitempty"`
	Snippet  string   `json:"snippet,omitempty"`
	// Body is the recorded observation, inlined on narrow answers.
	Body          []string `json:"body,omitempty"`
	BodyTruncated bool     `json:"body_truncated,omitempty"`
	// Untrusted marks external content and survives retrieval.
	Untrusted bool   `json:"untrusted,omitempty"`
	Trust     string `json:"trust,omitempty"`
	Verified  *bool  `json:"verified,omitempty"`
}

// Facets are counted dimensions over the answered generation.
type Facets struct {
	ByAgent map[string]int `json:"by_agent,omitempty"`
	ByLeg   map[string]int `json:"by_leg,omitempty"`
	ByKind  map[string]int `json:"by_kind,omitempty"`
	ByTool  map[string]int `json:"by_tool,omitempty"`
	// Exhaustive is false when facets were counted over a truncated generation.
	Exhaustive bool `json:"exhaustive"`
}

// Receipt is the survey receipt the prompt loop reads to track fruitless
// searching.
type Receipt struct {
	Tool          string `json:"tool"`
	PathsTouched  int    `json:"paths_touched"`
	BytesReturned int    `json:"bytes_returned"`
	Truncated     bool   `json:"truncated"`
	ScopeHash     string `json:"scope_hash"`
}
