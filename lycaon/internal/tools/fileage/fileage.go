// Package fileage measures file activity relative to the repository's tracked files.
package fileage

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// gitClient supplies tracked-file timestamps and revisions for cache refresh checks.
type gitClient interface {
	LastTouchByPath(ctx context.Context, projectDir string) (map[string]time.Time, error)
	HeadSHA(ctx context.Context, projectDir string) (string, error)
}

// Position is a file's place in the repo's activity distribution.
type Position struct {
	// LastChanged is the file's last-commit time.
	LastChanged time.Time
	// OlderThanPct is the share of tracked files changed more recently, as a
	// percent (0..100). 90 means the file is in the oldest ~10% of the repo.
	OlderThanPct int
	// TrackedFiles is the distribution size the percent is drawn from.
	TrackedFiles int
}

// Summary formats the date and activity percentile.
func (p Position) Summary() string {
	return fmt.Sprintf("last changed %s; older than ~%d%% of tracked files",
		p.LastChanged.Format("2006-01-02"), p.OlderThanPct)
}

// index is an immutable snapshot of the repo's last-touch distribution.
type index struct {
	byPath map[string]time.Time
	// sorted holds every file's last-touch time, ascending, for percentile
	// lookups by binary search.
	sorted []time.Time
}

func buildIndex(byPath map[string]time.Time) *index {
	sorted := make([]time.Time, 0, len(byPath))
	for _, t := range byPath {
		sorted = append(sorted, t)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })
	return &index{byPath: byPath, sorted: sorted}
}

func (idx *index) position(path string) (Position, bool) {
	t, ok := idx.byPath[path]
	if !ok {
		return Position{}, false
	}
	total := len(idx.sorted)
	if total <= 1 {
		// A percentile needs at least two tracked files.
		return Position{}, false
	}
	// Count files strictly more recent than this one: the first index whose
	// time is strictly greater than t, to the end.
	firstAfter := sort.Search(total, func(i int) bool { return idx.sorted[i].After(t) })
	moreRecent := total - firstAfter
	pct := int((float64(moreRecent)/float64(total))*100 + 0.5)
	return Position{LastChanged: t, OlderThanPct: pct, TrackedFiles: total}, true
}

type buildState int

const (
	stateIdle buildState = iota
	stateBuilding
	stateReady
	stateFailed
)

// DefaultTTL controls revision checks; unchanged revisions avoid a full rescan.
const DefaultTTL = 5 * time.Minute

// repoEntry holds one repository's background scan state.
type repoEntry struct {
	state      buildState
	idx        *index
	headSHA    string
	builtAt    time.Time
	refreshing bool
}

// Provider serves cached activity positions without waiting for scans.
// Unavailable scans and untracked paths yield no position.
type Provider struct {
	git gitClient
	ttl time.Duration
	now func() time.Time

	mu    sync.Mutex
	repos map[string]*repoEntry
}

// New returns nil when no git capability is available.
func New(git gitClient) *Provider {
	if git == nil {
		return nil
	}
	return &Provider{git: git, ttl: DefaultTTL, now: time.Now, repos: map[string]*repoEntry{}}
}

// Position starts missing scans in the background and returns only available facts.
func (p *Provider) Position(ctx context.Context, projectDir, path string) (Position, bool) {
	if p == nil {
		return Position{}, false
	}
	idx := p.indexOrTrigger(ctx, projectDir)
	if idx == nil {
		return Position{}, false
	}
	path = strings.ReplaceAll(strings.TrimSpace(path), `\`, `/`)
	return idx.position(path)
}

// Warm starts an idle repository scan in the background.
func (p *Provider) Warm(ctx context.Context, projectDir string) {
	if p == nil {
		return
	}
	if p.claimBuild(projectDir) {
		// The build outlives the warm call, so it keeps the caller's
		// context values without its cancellation.
		go p.build(context.WithoutCancel(ctx), projectDir)
	}
}

// Prewarm runs an idle repository scan synchronously.
func (p *Provider) Prewarm(ctx context.Context, projectDir string) {
	if p == nil {
		return
	}
	if p.claimBuild(projectDir) {
		p.build(ctx, projectDir)
	}
}

// Invalidate removes the cached distribution so the next read starts a scan.
func (p *Provider) Invalidate(projectDir string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.repos, projectDir)
	p.mu.Unlock()
}

// indexOrTrigger serves the current index while scheduling builds or TTL refreshes.
// Background work retains context values but outlives read cancellation.
func (p *Provider) indexOrTrigger(ctx context.Context, projectDir string) *index {
	bgCtx := context.WithoutCancel(ctx)
	p.mu.Lock()
	entry := p.repos[projectDir]
	if entry != nil && entry.state == stateReady {
		idx := entry.idx
		refresh := !entry.refreshing && p.now().Sub(entry.builtAt) > p.ttl
		if refresh {
			entry.refreshing = true
		}
		p.mu.Unlock()
		if refresh {
			go p.refresh(bgCtx, projectDir)
		}
		return idx
	}
	p.mu.Unlock()
	if p.claimBuild(projectDir) {
		go p.build(bgCtx, projectDir)
	}
	return nil
}

// claimBuild gives one caller ownership of an idle repository scan.
func (p *Provider) claimBuild(projectDir string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.repos[projectDir]
	if entry == nil {
		entry = &repoEntry{}
		p.repos[projectDir] = entry
	}
	if entry.state != stateIdle {
		return false
	}
	entry.state = stateBuilding
	return true
}

// build records scan results after git operations finish outside the lock.
func (p *Provider) build(ctx context.Context, projectDir string) {
	byPath, err := p.git.LastTouchByPath(ctx, projectDir)
	head, headErr := p.git.HeadSHA(ctx, projectDir)
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.repos[projectDir]
	if entry == nil {
		return // invalidated mid-build; drop the result
	}
	if err != nil || len(byPath) == 0 {
		entry.state = stateFailed
		return
	}
	entry.idx = buildIndex(byPath)
	if headErr == nil {
		entry.headSHA = head
	}
	entry.builtAt = p.now()
	entry.state = stateReady
}

// refresh rescans changed revisions while reads use the current index.
// Every attempt resets the TTL, including failures.
func (p *Provider) refresh(ctx context.Context, projectDir string) {
	head, headErr := p.git.HeadSHA(ctx, projectDir)

	p.mu.Lock()
	entry := p.repos[projectDir]
	if entry == nil {
		p.mu.Unlock()
		return // invalidated after the refresh was scheduled
	}
	prevSHA := entry.headSHA
	p.mu.Unlock()

	var newIdx *index
	newSHA := prevSHA
	if headErr == nil {
		if head == prevSHA && prevSHA != "" {
			newSHA = head // history unchanged — skip the rescan entirely
		} else if byPath, err := p.git.LastTouchByPath(ctx, projectDir); err == nil && len(byPath) > 0 {
			newIdx = buildIndex(byPath)
			newSHA = head
		}
		// A scan error leaves headSHA unchanged so the next cycle retries.
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	entry = p.repos[projectDir]
	if entry == nil {
		return // invalidated mid-refresh; drop the result
	}
	if newIdx != nil {
		entry.idx = newIdx
	}
	entry.headSHA = newSHA
	entry.builtAt = p.now()
	entry.refreshing = false
}
