package llm

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
)

// GlossLine is a capped navigation hint with no handle and no groundable claim.
type GlossLine struct {
	Label string `json:"label"`
}

// MaterializedSelection is one resolve-or-drop selection with verbatim materialized lines.
type MaterializedSelection struct {
	Triple     evidence.Triple     `json:"triple"`
	Resolution evidence.Resolution `json:"resolution"`
	Lines      []string            `json:"lines,omitempty"`
}

// CurationReport describes curator coverage and operational flags.
type CurationReport struct {
	Selected int  `json:"selected"`
	Total    int  `json:"total"`
	Dropped  int  `json:"dropped"`
	Retries  int  `json:"retries"`
	CacheHit bool `json:"cache_hit,omitempty"`
	Fallback bool `json:"fallback,omitempty"`
}

// CurationResult is the curator output: resolved verbatim selections + fenced gloss.
type CurationResult struct {
	Selections []MaterializedSelection `json:"selections,omitempty"`
	Gloss      []GlossLine             `json:"gloss,omitempty"`
	Report     CurationReport          `json:"report"`
}

// Curator selects verbatim evidence references from a candidate snapshot ledger.
// snapshot holds only the current tool's deterministic output, not the
// accumulated session ledger, so prior survey handles cannot be selected.
type Curator interface {
	Curate(ctx context.Context, snapshot evidence.Ledger, focus CurationFocus, budget int) (CurationResult, error)
}

const (
	defaultCurationBudget = 5
	maxGlossLines         = 5
	maxGlossLabelRunes    = 80
	curatorMaxRetries     = 1
)
