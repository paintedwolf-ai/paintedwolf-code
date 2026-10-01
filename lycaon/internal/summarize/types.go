package summarize

import (
	"context"
)

// Request is a validated summarize call.
type Request struct {
	Task                        string
	Content                     string   // inline material (may be empty)
	Path                        string   // repo-relative gather root (may be empty)
	Paths                       []string // explicit files/dirs (may be empty)
	Pattern                     string   // RE2 grep pattern (may be empty)
	Cursor                      string   // opaque continuation (may be empty)
	CursorPosition              string
	CursorMatchesObserved       int
	CursorMatchingFilesObserved int
	MaxAnchors                  int // already clamped to caps
}

// HasRepoKeys reports whether any repo-scope input is present.
func (r Request) HasRepoKeys() bool {
	return r.Path != "" || len(r.Paths) > 0 || r.Pattern != ""
}

// Candidate is one file or inline chunk.
type Candidate struct {
	// RelPath is the repo-relative path, or "inline" / "inline#N" for chunks.
	RelPath string
	// Kind is one of KindFile / KindInline.
	Kind string
	// ContentHash identifies the material the leaf covers.
	ContentHash string
	// StartLine is the first line represented by Body.
	Body      string
	StartLine int
}

// Anchor is a verbatim, line-cited span.
type Anchor struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
	Handle  string `json:"handle,omitempty"`
}

// NextAction is a concrete follow-up tool call.
type NextAction struct {
	TargetKind string   `json:"-"`
	Task       string   `json:"task,omitempty"`
	Tool       string   `json:"tool"`              // read | grep | find | list_dir | summarize
	Path       string   `json:"path,omitempty"`    // for read/find/list_dir
	Paths      []string `json:"paths,omitempty"`   // for multi-path summarize
	Lines      string   `json:"lines,omitempty"`   // e.g. "40-88" for read
	Pattern    string   `json:"pattern,omitempty"` // for grep
	Cursor     string   `json:"cursor,omitempty"`  // for summarize
	Why        string   `json:"why"`               // one line: what it gets you
}

// ContextPack is the tiered briefing material.
type ContextPack struct {
	Identity  []PackIdentity
	Skeleton  []PackSymbol
	Substance []PackWindow
	Imports   []PackImportEdge
	CallSites []PackCallSite
	Neighbors []PackNeighbor
	Gaps      []string
}

// PackIdentity orients the reduce: path, size, and parse health.
type PackIdentity struct {
	Path          string
	Kind          string // file | dir_map | inline
	LineCount     int
	ParseHealth   string // ok | no_symbols | degraded_headers | degraded_keys | degraded_paragraphs
	ContentHash   string
	Language      string
	OutlineSource string
	Parses        *bool
	Errors        []string // bounded syntax diags
	ErrorKind     string   // ErrorKindUnexpected | ErrorKindIncomplete (parse-flagged only)
	LogDigest     string
	ImportPath    string
}

const (
	// ErrorKindUnexpected marks an unexpected syntax token.
	ErrorKindUnexpected = "unexpected_syntax"
	// ErrorKindIncomplete marks an absent expected syntax token.
	ErrorKindIncomplete = "incomplete_syntax"
)

// PackSymbol is one ranked skeleton definition (or a directory-map row).
type PackSymbol struct {
	Path string
	Kind string
	Name string
	Line int
}

// PackWindow is a symbol-anchored source window (substance tier).
type PackWindow struct {
	Path      string
	StartLine int
	EndLine   int
	Symbol    string
	Body      string
}

// PackImportEdge is a one-hop module edge.
type PackImportEdge struct {
	From string
	To   string
	Kind string // outbound | inbound
}

type PackCallSite struct {
	Path    string
	Line    int
	Excerpt string
}

// PackNeighbor is a one-line neighbor stub.
type PackNeighbor struct {
	Path string
	Why  string
}

// CuratorStats is host-only telemetry for one assemble.
type CuratorStats struct {
	MetadataRowsRead     int
	DirectoryEntriesRead int
	DirectoriesOpened    int
	SourceFilesRead      int
	SourceBytesRead      int64
	BudgetTokensSpent    int
	BreadthAdmits        int
	DepthAdmits          int
	DeepenHits           int
	FitAdmits            int
	PrimaryLimitReason   string // pack_budget | none
	// Subtree allocator metrics; zero when the flat knapsack ran.
	ChildrenAdmitted int
	ChildrenDrilled  int
	ChildrenRolledUp int
	RecursionDepth   int
	// Importance tiebreak metrics; zero when signals off or unused.
	DoclinkBoosts int
	FaninBoosts   int
	// NestedReposPruned counts nested VCS roots excluded during gather.
	NestedReposPruned int
	// FilesOutlined counts on-demand outlines.
	FilesOutlined int
	// OutlinesSkippedRolledUp counts children represented without an outline.
	OutlinesSkippedRolledUp int
	// FaninGrepPasses counts indexed fan-in scans.
	FaninGrepPasses int
	// ForcedDrills counts minimum-depth admissions.
	ForcedDrills int
	// NameIndexed counts pre-drill outlines used only for task×name affinity.
	NameIndexed int
	// TaskDepthBoosts counts drills that beat equal-material rollups via task score.
	TaskDepthBoosts int
	// WireFitPasses counts in-memory wire-budget trims (0 = first fill fit).
	WireFitPasses int
}

// DefinitionItem is one rankable pile unit: a symbol+line (or degraded anchor).
type DefinitionItem struct {
	RelPath       string
	Kind          string
	Name          string
	Line          int
	EndLine       int
	Signature     string
	Doc           string
	Pinned        bool // Places directory maps ahead of ranked definitions.
	FileKind      string
	LineCount     int
	ParseHealth   string
	Head          string
	StartLine     int
	ContentHash   string
	Language      string
	OutlineSource string
	Parses        *bool
	Errors        []string
	ErrorKind     string
	LogDigest     string
	ImportPath    string
}

// Structure kind labels for gathered structural candidates.
const (
	StructureKindFile   = "file"
	StructureKindDirMap = "dir_map"
	StructureKindInline = "inline"
)

// Subtree kind labels for the scale-invariant material tree.
const (
	SubtreeKindFile = "file"
	SubtreeKindDir  = "dir"
)

// Material is a structural count under a subtree node.
type Material struct {
	Defs        int
	SourceFiles int
	Bytes       int64
}

// SubtreeNode is a catalog view under one target path.
type SubtreeNode struct {
	CatalogRootID   string
	UnknownMaterial bool
	Representative  string
	ChildCount      int
	Remainder       *SubtreeRemainder
	LoadChildren    func(context.Context, *SubtreeNode)
	Path            string
	Kind            string
	Children        []*SubtreeNode
	RollupOnly      bool // Represents a bounded branch without descending further.
	ScopePaths      []string
	Cursor          string
	CursorScope     string
	Revision        uint64 // source-catalog generation
	Material        Material
}

// SubtreeRemainder describes children outside a bounded metadata page.
type SubtreeRemainder struct {
	Children int
	Material Material
	Next     string
}

// StructureSymbol is one outline entry with a real line number.
type StructureSymbol struct {
	Kind string
	Name string
	Line int
	// Signature is the definition's own source line, bounded; it is what a
	// reader sees first and what the decision engine ranks on.
	Signature string
	// Doc is the comment directly above the definition, bounded.
	Doc string
}

// StructureCandidate is one deterministic structure unit.
type StructureCandidate struct {
	RelPath       string
	Kind          string
	LineCount     int
	Head          string
	StartLine     int
	Symbols       []StructureSymbol
	RollupRows    []string
	ContentHash   string
	Language      string
	OutlineSource string
	Parses        *bool
	Errors        []string
	ErrorKind     string
	LogDigest     string
	ImportPath    string
}

// FitEdges are optional one-hop context.
type FitEdges struct {
	Neighbors []PackNeighbor
	CallSites []PackCallSite
	Imports   []PackImportEdge
}

// Coverage states what the host examined and represented.
type Coverage struct {
	CatalogState          string
	CatalogRefreshing     bool
	Complete              bool
	CatalogRevision       uint64
	FilesTotal            int
	FilesRepresented      int
	DefinitionsTotal      int
	AnchorsReturned       int
	MatchesObserved       int
	MatchingFilesObserved int
	MatchSamplesReturned  int
	ChildrenTotal         int
	ChildrenReturned      int
	Cursor                string
	NextCursor            string
}

// Result is the assembled briefing and navigation.
type Result struct {
	Task          string
	Pack          ContextPack
	Anchors       []Anchor
	NextActions   []NextAction
	Coverage      Coverage
	Sources       []string
	Gather        GatherReport
	Orchestration OrchestrationReport
}

// PatternMatchSample is one retained pattern hit.
type PatternMatchSample struct {
	Path    string
	Line    int
	Content string // verbatim matched line (truncated when very long)
}

// GatherReport is operator/debug visibility into the gather phase.
type GatherReport struct {
	Mode          Mode
	Path          string
	Paths         []string
	Pattern       string
	Candidates    int
	Bytes         int
	MatchCount    int // pattern match lines examined
	SampleMatches []PatternMatchSample
}

// OrchestrationReport describes gather and assembly.
type OrchestrationReport struct {
	GatherMs   int
	AssembleMs int
	TotalMs    int
	Curator    CuratorStats
}

// Gatherer turns a request into bounded structure candidates.
type Gatherer interface {
	Gather(ctx context.Context, req Request) (GatherResult, error)
}

// GatherResult is the gather phase output.
type GatherResult struct {
	CatalogState          string
	CatalogRefreshing     bool
	Mode                  Mode
	CatalogRevision       uint64
	Candidates            []Candidate
	Structure             []StructureCandidate
	Fit                   FitEdges // candidate fit edges; curator admits under slack
	Stats                 GatherStats
	Bytes                 int
	MatchCount            int // pattern match lines examined (0 when no pattern)
	MatchingFilesObserved int
	CursorFound           bool
	NextCursorPath        string
	SampleMatches         []PatternMatchSample
	NextActions           []NextAction
	// Subtree is the indexed material tree for the requested scope.
	Subtree *SubtreeNode
	// Importance holds per-child ranking signals.
	Importance SubtreeImportance
}

// OutlineProvider outlines one repository path on demand.
type OutlineProvider interface {
	Outline(ctx context.Context, relPath string) (StructureCandidate, bool)
}
