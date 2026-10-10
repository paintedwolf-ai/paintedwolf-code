package sourceapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/symbolsearch"
)

const symbolProgressLimit = 16
const symbolProgressTTL = 2 * time.Minute

type symbolProgressCache struct {
	mu      sync.Mutex
	entries map[[32]byte]*symbolProgressEntry
}

type symbolProgressEntry struct {
	gate      chan struct{}
	users     int
	used      time.Time
	stamp     string
	state     symbolsearch.Progress
	discovery project.DeclarationSearch
}

type symbolSourceStamp struct {
	Root     string
	Revision uint64
	Instance uint64
	Epoch    repochange.Epoch
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
		status, err := sourcecatalog.Process().IndexStatus(ctx, p.ID, sourcecatalog.Root{ID: root.ID, Path: root.Path})
		if err != nil {
			return "", err
		}
		stamps = append(stamps, symbolSourceStamp{Root: root.Path, Revision: status.Revision, Instance: status.Instance, Epoch: repochange.CurrentEpoch(root.Path)})
	}
	body, err := json.Marshal(stamps)
	return string(body), err
}

// acquire serializes one query without blocking unrelated queries. Entries hold
// no readers, goroutines, or preparation ownership and expire on bounded LRU use.
func (c *symbolProgressCache) acquire(ctx context.Context, p *project.Project, leg *search.SymbolPlanLeg, roots []string) (*symbolProgressEntry, func(), error) {
	body, err := json.Marshal(struct {
		Project  string
		Attached []project.Root
		Name     string
		Query    any
		Roots    []string
		Flags    search.MatchFlags
		Excludes []string
	}{p.ID, p.Roots, leg.Name, symbolQueryIdentity(leg.Query), roots, leg.Flags, leg.ExcludeDirs})
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
	if entry.discovery == nil || entry.stamp != stamp {
		entry.state = symbolsearch.Progress{}
		entry.discovery = declarationSearchIn(leg.DiscoveryScope(), leg.Flags.Include, leg.Flags.Exclude)
		entry.stamp = stamp
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
