package search

import (
	"context"
	"time"
)

const (
	ExecutorStore  = "store"
	ExecutorCode   = "code"
	ExecutorSymbol = "symbol"
)

const (
	// SearchDisplayMaxHits bounds one interactive generation.
	SearchDisplayMaxHits = 20_000
	// SearchExecutorProbeHits detects generation truncation.
	SearchExecutorProbeHits = SearchDisplayMaxHits + 1
	// ExportMaxHits bounds one export generation.
	ExportMaxHits = 100_000
	// ExportExecutorProbeHits detects export truncation.
	ExportExecutorProbeHits = ExportMaxHits + 1
)

// PlanLeg is one executor arm of a routed plan.
type PlanLeg struct {
	Executor string
	Cap      int
	Store    *StorePlanLeg
	Code     *CodePlanLeg
	Symbol   *SymbolPlanLeg
}

// Hit is a merged search result row.
type Hit struct {
	MessageID   string
	ID          string
	HitKind     string
	Source      string
	Score       float64
	TS          string
	Snippet     string
	SourceRef   string
	SessionID   string
	ProjectID   string
	RootID      string
	ProjectName string
	LegID       string
	Handle      string
	// Tool names the producing tool on tool-call and tool-result rows.
	Tool     string
	Path     string
	Line     int
	URL      string
	Trust    string
	Verified *bool
	HintCode string
	// Worker coordinates locate child-session hits in their coordinator.
	ParentSessionID string
	WorkerID        string
	// AgentType is the producing session's role, joined at read time.
	AgentType string
	// Kind is the evidence sub-kind (read, grep, web …) behind HitKind.
	Kind string
	// Untrusted marks external content and travels with the row.
	Untrusted bool
	// Tombstoned rows survive a session delete carrying identity only.
	Tombstoned bool
	// Title and Context are host display projections.
	Title   string
	Context string
	// TitleHighlights are the code point ranges of Title the query matched.
	TitleHighlights []TextRange
	// SymbolKind is the declaration kind of a symbol hit.
	SymbolKind string
}

// TextRange is a half-open code point range.
type TextRange struct {
	Start int
	End   int
}

// ExecutorReport is one leg's outcome.
type ExecutorReport struct {
	Hits    []Hit
	Limited bool
	// SkippedFiles counts unreadable or oversized in-scope files.
	SkippedFiles int
	Issues       []Issue
	// TimedOut marks a leg whose wall-clock budget ended with work unscanned.
	TimedOut bool
	// Code carries the code leg's counters; zero for the store leg.
	Code CodeLegReport
	// Symbol carries the symbol leg's counters.
	Symbol SymbolLegReport
}

// SymbolLegReport is the symbol leg's per-request telemetry.
type SymbolLegReport struct {
	Projects int
	// FilesOutlined counts files whose outlines confirmed declarations.
	FilesOutlined int
	Declarations  int
}

// CodeLegReport is the code leg's per-request telemetry.
type CodeLegReport struct {
	// Warming roots have no reader; incomplete and refreshing roots remain readable.
	Roots             int
	RefreshingRoots   int
	IncompleteRoots   int
	WarmingRoots      int
	IndexWarmingRoots int
	// UnobservedDirs counts directories excluded by the discovery budget.
	UnobservedDirs     int
	FailedDirs         int
	RefreshFailedRoots int
	// Candidate and prefilter counts describe the content-matching phase.
	FilesListed       int
	ContentCandidates int
	FilesOpened       int
	PrefilterSkipped  int
	// IndexUsed marks candidate selection through the catalog literal index.
	IndexUsed      bool
	GenerationWait time.Duration
	IndexWait      time.Duration
}

// Executor runs one read-only leg of a routed plan.
type Executor interface {
	Source() string
	Run(ctx context.Context, leg PlanLeg) (ExecutorReport, error)
}
