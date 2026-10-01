package logoutline

import "errors"

// Digest summarizes a log stream for read outline output.
type Digest struct {
	Format      LogFormat   `json:"format"`
	RecordCount int         `json:"record_count"`
	ParsedCount int         `json:"parsed_count"`
	TimeSpan    *TimeSpan   `json:"time_span,omitempty"`
	Fields      []FieldStat `json:"fields,omitempty"`
	Facets      []Facet     `json:"facets,omitempty"`
	Clusters    []Cluster   `json:"clusters,omitempty"`
	Truncated   bool        `json:"truncated"`
}

type TimeSpan struct {
	Start string `json:"start_at"`
	End   string `json:"end_at"`
}

type FieldStat struct {
	Key      string `json:"key"`
	Coverage int    `json:"coverage_pct"`
}

type Facet struct {
	Key    string       `json:"key"`
	Values []FacetValue `json:"values"`
}

type FacetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type Cluster struct {
	Template  string `json:"template"`
	Count     int    `json:"count"`
	Severity  string `json:"severity,omitempty"`
	FirstLine int    `json:"first_line"`
	LastLine  int    `json:"last_line"`
}

var ErrNotLog = errors.New("logoutline: not a recognized log stream")
