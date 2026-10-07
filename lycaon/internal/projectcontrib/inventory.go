package projectcontrib

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/governance"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

const inventoryRoots = 16
const inventoryPaths = 512
const inventoryBytes = 16 << 20

type inventoryRoot struct {
	index     []governance.ResolvedAgentsMD
	dirty     map[string]struct{}
	full      bool
	ready     bool
	validated time.Time
	touched   time.Time
	flight    *inventoryFlight
}

type inventoryFlight struct {
	done  chan struct{}
	full  bool
	index []governance.ResolvedAgentsMD
	err   error
}

// Inventory caches discovery paths and captures fresh file bytes on each scan.
type Inventory struct {
	mu       sync.Mutex
	roots    map[string]*inventoryRoot
	stop     func()
	ctx      context.Context
	cancel   context.CancelFunc
	slots    chan struct{}
	walk     func(context.Context, string) ([]governance.ResolvedAgentsMD, error)
	coverage func(string) repochange.WatchCoverage
}

func NewInventory() *Inventory {
	i := &Inventory{roots: make(map[string]*inventoryRoot), slots: make(chan struct{}, 2), walk: governance.ListIndex, coverage: repochange.Coverage}
	i.ctx, i.cancel = context.WithCancel(context.Background())
	i.stop = repochange.RegisterObserver(i.changed)
	return i
}

var processInventory = sync.OnceValue(NewInventory)

func ProcessInventory() *Inventory { return processInventory() }

func (i *Inventory) Close() { i.cancel(); i.stop() }

// Scan joins a shared directory index with the current contributed bytes.
func (i *Inventory) Scan(ctx context.Context, roots []string) (Manifest, error) {
	return i.scan(ctx, roots, 0)
}

// ScanRecent reuses an index validated within maxAge even if changes marked it dirty.
// Status reads use it; reads that record or act on the inventory use Scan.
func (i *Inventory) ScanRecent(ctx context.Context, roots []string, maxAge time.Duration) (Manifest, error) {
	return i.scan(ctx, roots, maxAge)
}

func (i *Inventory) scan(ctx context.Context, roots []string, maxAge time.Duration) (Manifest, error) {
	roots = cleanRoots(roots)
	indices := make(map[string][]governance.ResolvedAgentsMD, len(roots))
	for _, root := range roots {
		index, err := i.read(ctx, root, maxAge)
		if err != nil {
			return Manifest{}, err
		}
		indices[root] = index
	}
	out := Manifest{Surfaces: make([]Surface, 0, len(registry))}
	totalBytes := 0
	captured := make(map[string]string)
	for _, row := range registry {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		surface := scanSurface(row.ID, roots, indices)
		if surface.ReadError != nil {
			return Manifest{}, surface.ReadError
		}
		for _, file := range surface.Files {
			key := file.RootPath + "/" + file.Path
			if prior, ok := captured[key]; ok && prior != file.SHA256 {
				return Manifest{}, errors.New("project configuration changed during capture; open Trust again")
			}
			captured[key] = file.SHA256
			if len(captured) > maxTrustSnapshotFiles {
				return Manifest{}, errors.New("project configuration exceeds the 4096 file review limit")
			}
			totalBytes += len(file.Content)
		}
		if totalBytes > maxTrustSnapshotBytes {
			return Manifest{}, errors.New("project configuration exceeds the 32 MB review limit")
		}
		out.Surfaces = append(out.Surfaces, surface)
	}
	return out, nil
}

func (i *Inventory) changed(_ context.Context, ev repochange.Event) {
	if ev.Kind == repochange.IndexChanged {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	for root, entry := range i.roots {
		projectRel, err := filepath.Rel(ev.ProjectDir, root)
		rootInsideEvent := err == nil && projectRel != ".." && !strings.HasPrefix(projectRel, ".."+string(filepath.Separator))
		eventRel, err := filepath.Rel(root, ev.ProjectDir)
		eventInsideRoot := err == nil && eventRel != ".." && !strings.HasPrefix(eventRel, ".."+string(filepath.Separator))
		if !rootInsideEvent && !eventInsideRoot {
			continue
		}
		if len(ev.Paths) == 0 || ev.Kind == repochange.HeadMoved {
			entry.full = true
			continue
		}
		for _, path := range ev.Paths {
			changed := filepath.Join(ev.ProjectDir, path)
			rel, err := filepath.Rel(root, changed)
			if err != nil {
				continue
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				ancestor, err := filepath.Rel(changed, root)
				if err == nil && ancestor != ".." && !strings.HasPrefix(ancestor, ".."+string(filepath.Separator)) {
					entry.full = true
				}
				continue
			}
			if len(entry.dirty) >= inventoryPaths {
				entry.full = true
				break
			}
			entry.dirty[filepath.ToSlash(rel)] = struct{}{}
		}
	}
}

func (i *Inventory) read(ctx context.Context, root string, maxAge time.Duration) ([]governance.ResolvedAgentsMD, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if canonical := fspath.CanonicalPath(root); canonical != "" {
		root = canonical
	}
	repochange.EnsureRoot(ctx, root)
	i.mu.Lock()
	entry := i.roots[root]
	if entry == nil {
		if len(i.roots) >= inventoryRoots*2 {
			i.mu.Unlock()
			return nil, errors.New("project inventory is busy; retry after discovery completes")
		}
		entry = &inventoryRoot{full: true, dirty: make(map[string]struct{})}
		i.roots[root] = entry
	}
	entry.touched = time.Now()
	coverage := i.coverage(root)
	// A full walk already in flight revalidates the root when it lands.
	revalidating := entry.flight != nil && entry.flight.full
	if (!coverage.Complete || !coverage.Recursive) && !revalidating && time.Since(entry.validated) >= repochange.CoverageRevalidationInterval {
		entry.full = true
	}
	if entry.ready && !entry.full && len(entry.dirty) == 0 && entry.flight == nil {
		index := slices.Clone(entry.index)
		i.mu.Unlock()
		return index, nil
	}
	// A recent index answers a status read; pending changes wait for the first read past the window.
	if entry.ready && maxAge > 0 && time.Since(entry.validated) < maxAge {
		index := slices.Clone(entry.index)
		i.mu.Unlock()
		return index, nil
	}
	flight := entry.flight
	if flight == nil {
		full, dirty, previous := entry.full, entry.dirty, entry.index
		if _, rootChanged := dirty["."]; rootChanged {
			full = true
		}
		flight = &inventoryFlight{done: make(chan struct{}), full: full}
		entry.flight = flight
		entry.full, entry.dirty = false, make(map[string]struct{})
		go i.refresh(context.WithoutCancel(ctx), root, entry, flight, full, dirty, previous)
	}
	i.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return slices.Clone(flight.index), flight.err
	}
}

func (i *Inventory) refresh(parent context.Context, root string, entry *inventoryRoot, flight *inventoryFlight, full bool, dirty map[string]struct{}, previous []governance.ResolvedAgentsMD) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(i.ctx, cancel) //nolint:contextcheck // Inventory shutdown cancels discovery shared by independently cancelable readers.
	defer stop()
	defer cancel()
	select {
	case i.slots <- struct{}{}:
		flight.index, flight.err = i.reconcile(ctx, root, full, dirty, previous)
		<-i.slots
	case <-ctx.Done():
		flight.err = ctx.Err()
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	entry.flight = nil
	if flight.err == nil {
		entry.index, entry.ready, entry.validated = flight.index, true, time.Now()
	} else {
		entry.full = true
	}
	close(flight.done)
	i.trim()
}

func (i *Inventory) reconcile(ctx context.Context, root string, full bool, dirty map[string]struct{}, previous []governance.ResolvedAgentsMD) ([]governance.ResolvedAgentsMD, error) {
	if full {
		return i.walk(ctx, root)
	}
	paths := make(map[string]struct{}, len(previous))
	for _, entry := range previous {
		paths[entry.Path] = struct{}{}
	}
	for rel := range dirty {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !inventoryPathVisible(rel) {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil && info.Mode().IsRegular() {
			if filepath.Base(rel) == protectedpath.AgentsMDFileName {
				paths[rel] = struct{}{}
			}
			continue
		}
		for path := range paths {
			if path == rel || strings.HasPrefix(path, rel+"/") {
				delete(paths, path)
			}
		}
		if err != nil || !info.IsDir() {
			continue
		}
		found, err := i.walk(ctx, abs)
		if err != nil {
			return nil, err
		}
		for _, entry := range found {
			paths[filepath.ToSlash(filepath.Join(rel, entry.Path))] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	slices.Sort(ordered)
	index := make([]governance.ResolvedAgentsMD, 0, len(ordered))
	for _, path := range ordered {
		index = append(index, governance.ResolvedAgentsMD{Path: path})
	}
	return index, nil
}

func inventoryPathVisible(path string) bool {
	if path == "." {
		return true
	}
	parts := strings.Split(path, "/")
	for n, part := range parts {
		if part == ".." || part == "" || sandbox.IsHiddenName(part) || governance.ListIndexOmitDir(part) || sandbox.ShouldSkipDir(strings.Join(parts[:n+1], "/"), part) {
			return false
		}
	}
	return true
}

func (i *Inventory) trim() {
	bytes := 0
	for _, entry := range i.roots {
		for _, path := range entry.index {
			bytes += len(path.Path) + 32
		}
	}
	for len(i.roots) > inventoryRoots || bytes > inventoryBytes {
		coldest := ""
		for root, entry := range i.roots {
			if entry.flight == nil && (coldest == "" || entry.touched.Before(i.roots[coldest].touched)) {
				coldest = root
			}
		}
		if coldest == "" {
			return
		}
		for _, path := range i.roots[coldest].index {
			bytes -= len(path.Path) + 32
		}
		delete(i.roots, coldest)
	}
}
