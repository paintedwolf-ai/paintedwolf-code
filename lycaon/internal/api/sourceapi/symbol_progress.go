package sourceapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/symbolsearch"
)

const symbolProgressLimit = 16
const symbolProgressTTL = 2 * time.Minute

// symbolRevalidateFileLimit bounds per-resume content revalidation so resuming
// retained progress stays bounded under normal symbol budgets; coverage whose
// retained files exceed this bound is discarded rather than reused unverified.
const symbolRevalidateFileLimit = symbolsearch.OutlineFileCap

type symbolProgressCache struct {
	mu      sync.Mutex
	entries map[[32]byte]*symbolProgressEntry
}

type symbolProgressEntry struct {
	retained  bool
	gate      chan struct{}
	users     int
	used      time.Time
	stamp     string
	state     symbolsearch.Progress
	discovery projectsource.DeclarationSearch
}

func (e *symbolProgressEntry) reset(stamp string, discovery projectsource.DeclarationSearch) {
	e.state = symbolsearch.Progress{}
	e.discovery = discovery
	e.stamp = stamp
}

// matchFilesCurrent re-observes every file contributing retained matches
// through the root-confined projectsource reader and confirms its raw content
// SHA-256 still matches the digest captured at the exact source read that
// produced those declarations.
func (e *symbolProgressEntry) matchFilesCurrent(ctx context.Context, p *project.Project) bool {
	digests := e.state.RetainedFileDigests()
	if len(digests) == 0 {
		return true
	}
	if len(digests) > symbolRevalidateFileLimit {
		return false
	}
	for key, wantSHA := range digests {
		if ctx.Err() != nil || wantSHA == "" {
			return false
		}
		obs, err := projectsource.ObserveProjectSource(p, projectsource.SourceReadRequest{
			RootID: key.RootID,
			Path:   key.Path,
		})
		if err != nil || obs.OverLimit || obs.Revision.SHA256 != wantSHA {
			return false
		}
	}
	return true
}

type symbolSourceStamp struct {
	Root        string
	Revision    uint64
	Instance    uint64
	Epoch       repochange.Epoch
	ValidatedAt time.Time
}

func symbolStamp(ctx context.Context, p *project.Project, rootIDs []string) (string, error) {
	var stamps []symbolSourceStamp
	for _, root := range p.Roots {
		selected := len(rootIDs) == 0
		for _, id := range rootIDs {
			selected = selected || id == root.ID
		}
		if !selected {
			continue
		}
		status, err := sourcecatalog.Process().Trees.IndexStatus(ctx, p.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path})
		if err != nil {
			return "", err
		}
		stamps = append(stamps, symbolSourceStamp{Root: root.Path, Revision: status.Revision, Instance: status.Instance, Epoch: repochange.CurrentEpoch(root.Path), ValidatedAt: status.ValidatedAt})
	}
	body, err := json.Marshal(stamps)
	return string(body), err
}

// acquire serializes one query without blocking unrelated queries. Entries hold
// no readers, goroutines, or preparation jobs and expire on bounded LRU use.
func (c *symbolProgressCache) acquire(ctx context.Context, p *project.Project, leg *search.SymbolPlanLeg, roots []string, retain bool) (*symbolProgressEntry, func(), error) {
	if !retain {
		return unretainedSymbolProgress(ctx, p, leg, roots)
	}
	body, err := json.Marshal(struct {
		Project             string
		Attached            []project.Root
		Name                string
		Query               any
		Roots               []string
		Flags               search.MatchFlags
		Excludes            []string
		IncludeDependencies bool
	}{p.ID, p.Roots, leg.Name, symbolQueryIdentity(leg.Query), roots, leg.Flags, leg.ExcludeDirs, leg.IncludeDependencies})
	if err != nil {
		return nil, nil, err
	}
	key := sha256.Sum256(body)
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[[32]byte]*symbolProgressEntry{}
	}
	now := time.Now()
	var oldest [32]byte
	found := false
	for k, item := range c.entries {
		if item.users > 0 {
			continue
		}
		if now.Sub(item.used) >= symbolProgressTTL {
			delete(c.entries, k)
			continue
		}
		if !found || item.used.Before(c.entries[oldest].used) {
			oldest, found = k, true
		}
	}
	entry := c.entries[key]
	if entry == nil {
		if len(c.entries) >= symbolProgressLimit && found {
			delete(c.entries, oldest)
		}
		entry = &symbolProgressEntry{gate: make(chan struct{}, 1), used: now}
		entry.gate <- struct{}{}
		if len(c.entries) < symbolProgressLimit {
			c.entries[key] = entry
			entry.retained = true
		}
	}
	entry.used = now
	entry.users++
	c.mu.Unlock()
	withdraw := func() {
		c.mu.Lock()
		entry.users--
		entry.used = time.Now()
		c.mu.Unlock()
	}
	select {
	case <-entry.gate:
	case <-ctx.Done():
		withdraw()
		return nil, nil, ctx.Err()
	}
	release := func() { entry.gate <- struct{}{}; withdraw() }
	stamp, err := symbolStamp(ctx, p, roots)
	if err != nil {
		release()
		return nil, nil, err
	}
	if entry.discovery == nil || entry.stamp != stamp || !symbolEpochsCurrent(entry.stamp) || !entry.matchFilesCurrent(ctx, p) {
		entry.reset(stamp, declarationSearchIn(leg.DiscoveryScope(), leg.Flags.Include, leg.Flags.Exclude, leg.IncludeDependencies))
	}
	return entry, release, nil
}

func symbolEpochsCurrent(stamp string) bool {
	var sources []symbolSourceStamp
	if json.Unmarshal([]byte(stamp), &sources) != nil {
		return false
	}
	for _, source := range sources {
		if !repochange.EpochCurrent(source.Root, source.Epoch) {
			return false
		}
		// Incomplete watches trust an observed generation only within the catalog
		// reconciliation interval; discovery coverage gaps remain independent.
		if !repochange.Coverage(source.Root).Complete &&
			(source.ValidatedAt.IsZero() || time.Since(source.ValidatedAt) > repochange.CoverageRevalidationInterval) {
			return false
		}
	}
	return true
}

// Tag every AST node: AND and OR deliberately have the same Go field shape.
func symbolQueryIdentity(node search.Node) any {
	switch n := node.(type) {
	case search.AndExpr:
		children := make([]any, 1, len(n.Exprs)+1)
		children[0] = "and"
		for _, child := range n.Exprs {
			children = append(children, symbolQueryIdentity(child))
		}
		return children
	case search.OrExpr:
		children := make([]any, 1, len(n.Exprs)+1)
		children[0] = "or"
		for _, child := range n.Exprs {
			children = append(children, symbolQueryIdentity(child))
		}
		return children
	case search.NotExpr:
		return []any{"not", symbolQueryIdentity(n.Expr)}
	case search.FilterExpr:
		return []any{"filter", n.Field, n.Value}
	case search.TextExpr:
		return []any{"text", n.Text, n.Phrase}
	case nil:
		return nil
	default:
		return fmt.Sprintf("%#v", node)
	}
}

// A stable federated leg reserves progress for its first bounded project set.
// Overflow can still finish in the foreground; unfinished work is a terminal
// retention bound because another request would restart the same slice.
func symbolProgressGap(retained bool) projectsource.DeclarationGap {
	if retained {
		return projectsource.DeclarationGap{Reason: projectsource.DeclarationPending}
	}
	return projectsource.DeclarationGap{Reason: projectsource.DeclarationSymbolBudget, Limit: symbolProgressLimit}
}

func unretainedSymbolProgress(ctx context.Context, p *project.Project, leg *search.SymbolPlanLeg, roots []string) (*symbolProgressEntry, func(), error) {
	stamp, err := symbolStamp(ctx, p, roots)
	if err != nil {
		return nil, nil, err
	}
	entry := &symbolProgressEntry{
		stamp:     stamp,
		discovery: declarationSearchIn(leg.DiscoveryScope(), leg.Flags.Include, leg.Flags.Exclude, leg.IncludeDependencies),
	}
	return entry, func() {}, nil
}
