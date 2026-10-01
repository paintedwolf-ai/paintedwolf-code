package recall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/search"
)

const (
	// DefaultLimit is the answer size when the caller states none.
	DefaultLimit = 20
	// MaxLimit bounds one answer. Paging is by narrowing, not by cursor: a
	// search generation expires in minutes, an agent turn need not.
	MaxLimit = 50
	// BodyInlineMaxHits is the widest answer that still carries bodies. Above
	// it the answer is snippets and facets.
	BodyInlineMaxHits = 8
	// BodyMaxLines bounds one inlined body.
	BodyMaxLines = 80
	// SnippetMaxChars bounds one snippet.
	SnippetMaxChars = 400
)

// ToolName is the agent-facing name.
const ToolName = "recall"

// Service answers recall requests over the store projection.
type Service struct {
	db      db.Handle
	router  *search.Router
	dataDir string
}

// NewService builds a store-only recall service. The nil code and symbol
// executors make a stray live-source leg a failure rather than an unrequested
// tree walk.
func NewService(database db.Handle, dataDir string) *Service {
	return &Service{
		db:      database,
		router:  search.NewRouter(search.NewStoreExecutor(database), nil, nil),
		dataDir: dataDir,
	}
}

// Request is one recall call.
type Request struct {
	Query  string
	Widen  Widen
	Limit  int
	Caller Caller
}

// Answer runs one recall and states a total resolution.
func (s *Service) Answer(ctx context.Context, req Request) (*Result, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("recall: service unavailable")
	}
	if err := validateQuery(req.Query); err != nil {
		return nil, err
	}
	scope, err := resolveScope(req.Caller, req.Widen)
	if err != nil {
		return nil, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	plan, err := search.CompileQuery(strings.TrimSpace(req.Query), s.compileContext(scope))
	if err != nil {
		return nil, err
	}
	// The cap is a probe: one row past the answer proves there is more. The
	// router trims it and records the limit, so truncation is read from the
	// result rather than re-derived from the count.
	if plan.Store != nil {
		plan.Store.Cap = limit + 1
	}
	result, err := s.router.Execute(ctx, plan, limit)
	if err != nil {
		return nil, err
	}

	live, removed := partitionTombstones(result.Hits)
	truncated := !result.Exhaustive

	out := &Result{
		ScopeStated: scope.Stated,
		QueryStated: statedQuery(result.Interpretation),
		Removed:     removed,
		Truncated:   truncated,
		Issues:      issueStrings(result.Issues),
	}
	if len(out.Issues) > 0 {
		// Partial rows are kept; only the completeness claim is withdrawn.
		out.Resolution = ResolutionExecutorDegraded
		out.NextAction = "The index did not fully answer. Do not read this as absence — retry, or ask the leg that would know."
		var bodyIssues []string
		out.Hits, bodyIssues = s.decorate(ctx, req.Caller, scope, live)
		out.Issues = append(out.Issues, bodyIssues...)
		out.Count = Count{Value: len(out.Hits), Relation: RelationAtLeast}
		out.Receipt = receiptFor(req, scope, len(out.Hits), bodyBytes(out.Hits), true)
		return out, nil
	}

	if len(live) == 0 {
		return s.emptyResult(ctx, req, scope, out, removed)
	}

	hits, bodyIssues := s.decorate(ctx, req.Caller, scope, live)
	out.Resolution = ResolutionMatched
	out.Hits = hits
	out.Count = Count{Value: len(hits), Relation: relationFor(truncated)}
	out.Facets = facetsFor(hits, truncated)
	if truncated {
		out.NextAction = "More rows matched than were returned. Narrow with agent:, leg:, tool:, or after: rather than repeating this query."
	}
	if removed > 0 {
		out.NextAction = strings.TrimSpace(out.NextAction + " Some matching rows belong to deleted sessions and carry no content.")
	}
	if len(bodyIssues) > 0 {
		out.Resolution = ResolutionExecutorDegraded
		out.Issues = append(out.Issues, bodyIssues...)
		out.NextAction = "Some recorded bodies could not be loaded. Returned excerpts are partial evidence, not proof that a detail was never observed."
	}
	out.Receipt = receiptFor(req, scope, len(hits), bodyBytes(hits), truncated)
	return out, nil
}

// emptyResult separates an unmatched query from an unpopulated scope.
func (s *Service) emptyResult(ctx context.Context, req Request, scope Scope, out *Result, removed int) (*Result, error) {
	out.Count = Count{Value: 0, Relation: RelationExact}
	out.Receipt = receiptFor(req, scope, 0, 0, false)

	if removed > 0 {
		out.Resolution = ResolutionRecordDeleted
		out.NextAction = "That record existed and its session was deleted; the content is gone. Re-observe it directly, or proceed without it."
		return out, nil
	}
	populated, err := s.scopePopulated(ctx, scope)
	if err != nil {
		return nil, err
	}
	if !populated {
		out.Resolution = ResolutionScopeEmpty
		out.NextAction = "Nothing in this scope is indexed yet — no leg here has observed anything. Dispatch the work rather than searching for it again."
		return out, nil
	}
	out.Resolution = ResolutionNoMatchInScope
	out.NextAction = "This scope holds records; none matched. Try different terms once, then dispatch a leg or state the gap — repeating this query will not change the answer."
	return out, nil
}

func (s *Service) compileContext(scope Scope) search.CompileContext {
	return search.CompileContext{
		OriginProjectID:  scope.ProjectID,
		ProjectScope:     scope.ProjectID,
		SessionScope:     scope.Sessions,
		RootSessionScope: scope.RootSession,
		StoreOnly:        true,
		// Tombstones are partitioned out below rather than filtered in SQL, so a
		// deleted record stays distinguishable from one that never existed.
		IncludeTombstoned: true,
		Budget:            search.BudgetInteractive,
	}
}

// scopePopulated runs only when a query matched nothing.
func (s *Service) scopePopulated(ctx context.Context, scope Scope) (bool, error) {
	where := []string{"tombstoned = 0"}
	args := []any{}
	if project := strings.TrimSpace(scope.ProjectID); project != "" {
		where = append(where, "project_id = ?")
		args = append(args, project)
	}
	if len(scope.Sessions) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(scope.Sessions)), ", ")
		where = append(where, "session_id IN ("+placeholders+")")
		for _, id := range scope.Sessions {
			args = append(args, id)
		}
	}
	if root := strings.TrimSpace(scope.RootSession); root != "" {
		where = append(where, "root_session_id = ?")
		args = append(args, root)
	}
	query := "SELECT EXISTS(SELECT 1 FROM evidence_index WHERE " + strings.Join(where, " AND ") + ")"
	var exists int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&exists); err != nil {
		return false, fmt.Errorf("recall: probe scope: %w", err)
	}
	return exists != 0, nil
}

func partitionTombstones(hits []search.Hit) (live []search.Hit, removed int) {
	live = make([]search.Hit, 0, len(hits))
	for _, h := range hits {
		if h.Tombstoned {
			removed++
			continue
		}
		live = append(live, h)
	}
	return live, removed
}

func relationFor(truncated bool) string {
	if truncated {
		return RelationAtLeast
	}
	return RelationExact
}

func issueStrings(issues []search.Issue) []string {
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.Reason == search.IssueExecutorError {
			out = append(out, issue.Executor+": executor_error")
		}
	}
	return out
}

func statedQuery(in search.SearchInterpretation) string {
	parts := make([]string, 0, len(in.Filters)+len(in.FTSTerms))
	for _, f := range in.Filters {
		part := f.Field + ":" + f.Value
		if f.Negated {
			part = "NOT " + part
		}
		parts = append(parts, part)
	}
	for _, term := range in.FTSTerms {
		parts = append(parts, `"`+term+`"`)
	}
	return strings.Join(parts, " AND ")
}

func facetsFor(hits []Hit, truncated bool) *Facets {
	f := &Facets{
		ByAgent:    map[string]int{},
		ByLeg:      map[string]int{},
		ByKind:     map[string]int{},
		ByTool:     map[string]int{},
		Exhaustive: !truncated,
	}
	for _, h := range hits {
		countInto(f.ByAgent, h.AgentType)
		countInto(f.ByLeg, h.LegID)
		countInto(f.ByKind, h.Kind)
		countInto(f.ByTool, h.Tool)
	}
	return f
}

func countInto(m map[string]int, key string) {
	if key = strings.TrimSpace(key); key != "" {
		m[key]++
	}
}

func bodyBytes(hits []Hit) int {
	total := 0
	for _, h := range hits {
		for _, line := range h.Body {
			total += len(line)
		}
		total += len(h.Snippet)
	}
	return total
}

func receiptFor(req Request, scope Scope, paths, bytes int, truncated bool) Receipt {
	return Receipt{
		Tool:          ToolName,
		PathsTouched:  paths,
		BytesReturned: bytes,
		Truncated:     truncated,
		ScopeHash:     scopeHash(req.Query, scope),
	}
}

func scopeHash(query string, scope Scope) string {
	sessions := append([]string(nil), scope.Sessions...)
	sort.Strings(sessions)
	sum := sha256.Sum256([]byte(strings.Join(append([]string{
		strings.TrimSpace(query), string(scope.Widen), scope.ProjectID, scope.RootSession,
	}, sessions...), "\x00")))
	return hex.EncodeToString(sum[:8])
}
