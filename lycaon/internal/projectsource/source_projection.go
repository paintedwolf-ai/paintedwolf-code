package projectsource

import (
	"context"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
)

const (
	sourceProjectionDirCap   = 10_000
	sourceProjectionChildCap = 100_000
)

type sourceProjectionKey struct {
	workspaceID string
	rootID      string
	rootPath    string
	dir         string
}

type projectedSourceListing struct {
	listing  SourceDirListing
	epoch    repochange.Epoch
	lastUsed time.Time
	stale    bool
}

type sourceProjectionFlight struct {
	done chan struct{}
}

type sourceDirectoryProjection struct {
	mu       sync.Mutex
	listings map[sourceProjectionKey]projectedSourceListing
	flights  map[sourceProjectionKey]*sourceProjectionFlight
	children int
}

var (
	sourceProjectionOnce sync.Once
	sourceProjection     *sourceDirectoryProjection
)

func processSourceProjection() *sourceDirectoryProjection {
	sourceProjectionOnce.Do(func() {
		sourceProjection = &sourceDirectoryProjection{
			listings: make(map[sourceProjectionKey]projectedSourceListing),
			flights:  make(map[sourceProjectionKey]*sourceProjectionFlight),
		}
		repochange.RegisterObserver(func(_ context.Context, event repochange.Event) {
			sourceProjection.invalidate(event)
		})
	})
	return sourceProjection
}

// BrowseProjectSource lists a directory through the shared projection.
func BrowseProjectSource(p ProjectSource, rootID, dir string) (SourceDirListing, error) {
	root, err := resolveSourceBrowseRoot(p, rootID)
	if err != nil {
		return SourceDirListing{}, err
	}
	dir, err = normalizeSourceBrowseDir(dir)
	if err != nil {
		return SourceDirListing{}, err
	}
	key := sourceProjectionKey{
		workspaceID: p.WorkspaceID(), rootID: root.root.ID,
		rootPath: filepath.Clean(root.path), dir: dir,
	}
	return processSourceProjection().get(key, root)
}

func (p *sourceDirectoryProjection) get(
	key sourceProjectionKey,
	root sourceBrowseRoot,
) (SourceDirListing, error) {
	for {
		now := time.Now()
		p.mu.Lock()
		if cached, ok := p.listings[key]; ok && projectedListingFresh(cached, key) {
			cached.lastUsed = now
			p.listings[key] = cached
			listing := cloneSourceDirListing(cached.listing)
			p.mu.Unlock()
			return listing, nil
		}
		if flight := p.flights[key]; flight != nil {
			done := flight.done
			p.mu.Unlock()
			<-done
			continue
		}
		flight := &sourceProjectionFlight{done: make(chan struct{})}
		p.flights[key] = flight
		p.mu.Unlock()

		listing, epoch, cacheable, err := observeSourceListing(root, key.workspaceID, key.dir)
		p.mu.Lock()
		delete(p.flights, key)
		completedAt := time.Now()
		if err == nil && cacheable && listing.WatchComplete {
			p.storeLocked(key, projectedSourceListing{
				listing: cloneSourceDirListing(listing), epoch: epoch,
				lastUsed: completedAt,
			})
			p.evictLocked()
		}
		close(flight.done)
		p.mu.Unlock()
		return listing, err
	}
}

func observeSourceListing(
	root sourceBrowseRoot,
	workspaceID, dir string,
) (SourceDirListing, repochange.Epoch, bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		before := repochange.CurrentEpoch(root.path)
		listing, _, err := browseProjectSourceAt(root, dir)
		if err != nil {
			return SourceDirListing{}, repochange.Epoch{}, false, err
		}
		after := repochange.CurrentEpoch(root.path)
		listing.WorkspaceID = workspaceID
		listing.WatchComplete = repochange.DirWatched(root.path, listing.Dir)
		if before == after {
			return listing, after, true, nil
		}
		if attempt == 2 {
			return listing, after, false, nil
		}
	}
	return SourceDirListing{}, repochange.Epoch{}, false, ErrSourceNotFound
}

// Only live directory watches can establish cached membership freshness.
func projectedListingFresh(
	entry projectedSourceListing,
	key sourceProjectionKey,
) bool {
	if entry.stale {
		return false
	}
	if !repochange.EpochCurrent(key.rootPath, entry.epoch) {
		return false
	}
	return repochange.DirWatched(key.rootPath, key.dir)
}

func (p *sourceDirectoryProjection) invalidate(event repochange.Event) {
	if p == nil || event.ProjectDir == "" {
		return
	}
	rootPath := filepath.Clean(event.ProjectDir)
	dirs := invalidatedSourceDirs(event.Paths)
	p.mu.Lock()
	for key, listing := range p.listings {
		if key.rootPath != rootPath {
			continue
		}
		if len(dirs) == 0 {
			listing.stale = true
			p.listings[key] = listing
			continue
		}
		if _, ok := dirs[key.dir]; ok {
			listing.stale = true
			p.listings[key] = listing
		}
	}
	p.mu.Unlock()
}

func invalidatedSourceDirs(paths []string) map[string]struct{} {
	dirs := make(map[string]struct{})
	for _, raw := range paths {
		rel := strings.TrimPrefix(path.Clean("/"+filepath.ToSlash(strings.TrimSpace(raw))), "/")
		if rel == "" || rel == "." {
			return nil
		}
		for dir := normalizeSourceDir(path.Dir(rel)); ; dir = normalizeSourceDir(path.Dir(dir)) {
			dirs[dir] = struct{}{}
			if dir == "." {
				break
			}
		}
	}
	return dirs
}

func (p *sourceDirectoryProjection) evictLocked() {
	if len(p.listings) <= sourceProjectionDirCap && p.children <= sourceProjectionChildCap {
		return
	}
	type candidate struct {
		key      sourceProjectionKey
		lastUsed time.Time
		children int
	}
	candidates := make([]candidate, 0, len(p.listings))
	for key, listing := range p.listings {
		candidates = append(candidates, candidate{key: key, lastUsed: listing.lastUsed, children: len(listing.listing.Entries)})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].lastUsed.Before(candidates[j].lastUsed) })
	for _, candidate := range candidates {
		if len(p.listings) <= sourceProjectionDirCap && p.children <= sourceProjectionChildCap {
			break
		}
		delete(p.listings, candidate.key)
		p.children -= candidate.children
	}
}

func (p *sourceDirectoryProjection) storeLocked(key sourceProjectionKey, listing projectedSourceListing) {
	if previous, ok := p.listings[key]; ok {
		p.children -= len(previous.listing.Entries)
	}
	p.listings[key] = listing
	p.children += len(listing.listing.Entries)
}

func cloneSourceDirListing(listing SourceDirListing) SourceDirListing {
	listing.Entries = append([]SourceDirEntry(nil), listing.Entries...)
	return listing
}
