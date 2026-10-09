package search

import "time"

// ProjectScopeMode describes how the query narrows project visibility.
type ProjectScopeMode string

const (
	ScopeGlobal  ProjectScopeMode = "global"
	ScopeCurrent ProjectScopeMode = "current"
	ScopeSlug    ProjectScopeMode = "slug"
)

// InterpretedFilter is one recognized filter, with its polarity preserved so
// clients can echo the query without dropping NOT.
type InterpretedFilter struct {
	Field   string
	Value   string
	Negated bool
}

// SearchInterpretation is the human-facing summary of what the compiler ran.
type SearchInterpretation struct {
	Scope    ProjectScopeMode
	Slug     string
	FTSTerms []string
	Filters  []InterpretedFilter
	// DependencyTreesExcluded marks a content scan that skipped dependency
	// and build trees; zero results does not cover them.
	DependencyTreesExcluded bool
}

// StorePlanLeg is the parameterized SQL/FTS store executor arm.
type StorePlanLeg struct {
	SQL  string
	Args []any
	Cap  int
	// Post applies match flags and path globs to SQL candidates.
	Post *StorePostFilter
	// PostScanCap bounds rows examined before post-filtering.
	PostScanCap int
}

// CodeRoot identifies an attached root for search and replacement.
type CodeRoot struct {
	ProjectID string
	// RootID is the registry identity shared with the source catalog.
	RootID string
	Path   string
}

// CodePlanLeg is the live code executor arm.
type CodePlanLeg struct {
	IncludeDependencies bool
	Query               Node
	PathRoots           []CodeRoot
	Cap                 int
	// Lines emits content matches; Files emits path matches.
	Lines bool
	Files bool
	// FileCap bounds path matches independently of content matches.
	FileCap int
	// FileExcludeDirs removes dependency and build trees from path matches.
	FileExcludeDirs []string
	// LineExcludeDirs removes dependency and build trees from content matches.
	LineExcludeDirs []string
	// Match flags are applied to each free-text leaf in Query.
	Flags MatchFlags
	// Budget sets the leg's wall-clock bound. Empty is complete.
	Budget SearchBudget
	// Wall replaces the budget's wall clock when positive.
	Wall time.Duration
}

// SymbolPlanLeg is the declaration-name arm: declarations whose names match
// Name, found by content discovery and confirmed by outline analysis.
type SymbolPlanLeg struct {
	IncludeDependencies bool
	// Query carries the filters and negated terms each declaration must satisfy.
	Query Node
	// Name is the query's one positive term.
	Name  string
	Roots []CodeRoot
	// Cap bounds declarations per project.
	Cap int
	// ExcludeDirs removes dependency and build trees from discovery.
	ExcludeDirs []string
	Flags       MatchFlags
	Budget      SearchBudget
}

// RoutedPlan is the compiled query plan; federation executes it.
type RoutedPlan struct {
	Store              *StorePlanLeg
	Code               *CodePlanLeg
	Symbol             *SymbolPlanLeg
	Executors          []string
	OriginProjectID    string
	Scope              ProjectScopeMode
	ScopeProjectArgIdx int
	OriginRankArgIdx   int
	Interpretation     SearchInterpretation
}

// CompileContext supplies origin bias and project scope resolution at compile time.
type CompileContext struct {
	IncludeDependencies  bool
	OriginProjectID      string
	ResolveProjectBySlug func(slug string) (projectID string, err error)
	RootsForProject      func(projectID string) ([]CodeRoot, error)
	AttachedProjectIDs   func() ([]string, error)
	// DependencyPathPatterns come from the scanners path-exclude catalog.
	DependencyPathPatterns []string
	// Flags apply to free-text terms on every leg.
	Flags MatchFlags
	// Budget is interactive or complete. Empty is complete.
	Budget SearchBudget
	// ProjectScope narrows store hits to one project without a query token.
	ProjectScope string
	// SessionScope bounds store hits to these session ids. Empty is unbounded.
	// IDs are bound SQL values.
	SessionScope []string
	// RootSessionScope bounds store hits to one session tree by the root stamped
	// at index time. Live parent links would miss legs whose session rows are gone.
	RootSessionScope string
	// StoreOnly drops the live-code leg.
	StoreOnly bool
	// IncludeTombstoned includes retained addresses of deleted sessions.
	IncludeTombstoned bool
}
