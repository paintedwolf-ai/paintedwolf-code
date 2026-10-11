package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
)

type DirectoryObservation struct {
	baseSequence          int64
	Path                  string
	Sequence, FirstListed int64
	Entries               int
	Invalidation          uint64
	Complete              bool
	Observed              time.Time
	Epoch                 repochange.Epoch
	// Stamp is the directory's own times when its listing began. A restored
	// checkpoint trusts a listing only while the directory still carries it.
	Stamp   DirectoryStamp
	Failure string
}
type DirectoryRead struct {
	Priority backgroundwork.Priority
	Entries  int
}

func (s *indexStore) readObservation(ctx context.Context, dir string) (DirectoryObservation, error) {
	if err := ctx.Err(); err != nil {
		return DirectoryObservation{}, err
	}
	pin, err := s.retainGeneration(headGeneration, true)
	if err != nil {
		return DirectoryObservation{}, err
	}
	defer pin.Release()
	record, found, err := pin.value.directories.Get(ctx, dir)
	if err != nil {
		return DirectoryObservation{}, err
	}
	if !found {
		return DirectoryObservation{}, pagedview.ErrMissing
	}
	return record.observation, nil
}

func (c *Directories) ObserveDirectory(ctx context.Context, projectID string, root Root, dir string, request DirectoryRead) (DirectoryObservation, error) {
	if err := validateObservationDirectory(root, dir); err != nil {
		return DirectoryObservation{}, err
	}
	store, err := c.trees.indexStore(ctx, projectID, root)
	if err != nil {
		return DirectoryObservation{}, err
	}
	return c.observeUntil(ctx, store, normalizeDir(dir), request.Entries, request.Priority)
}

func (c *Directories) observeUntil(ctx context.Context, store *indexStore, dir string, limit int, priority backgroundwork.Priority) (DirectoryObservation, error) {
	releaseInterest := store.observationInterests.join(dir, priority)
	defer releaseInterest()
	for attempt := 0; attempt < 4; attempt++ {
		observation, err := store.observations.Join(ctx, dir, limit, func(value DirectoryObservation) int { return value.Entries }, func(ctx context.Context, publish func(DirectoryObservation), demand func() int) (DirectoryObservation, error) {
			observation, err := c.observeDirectory(ctx, store, dir, publish, demand)
			if err != nil && ctx.Err() == nil && !errors.Is(err, os.ErrInvalid) && !errors.Is(err, errObservationChanged) && !errors.Is(err, backgroundwork.ErrSuperseded) {
				if recorded := store.recordObservationFailure(ctx, observation, err); recorded != nil {
					return observation, errors.Join(err, recorded)
				}
				observation.Failure = err.Error()
			}
			return observation, err
		})
		if errors.Is(err, errObservationChanged) || errors.Is(err, backgroundwork.ErrSuperseded) {
			continue
		}
		if err != nil || observation.Complete || limit > 0 && observation.Entries >= limit {
			return observation, err
		}
	}
	return DirectoryObservation{Path: dir}, errObservationChanged
}

func (c *Directories) observeDirectory(ctx context.Context, store *indexStore, dir string, publish func(DirectoryObservation), demand func() int) (DirectoryObservation, error) {
	cached, err := store.readObservation(ctx, dir)
	if err == nil && store.policy.boundaryPath(dir, true) == "" && store.observationFresh(cached) {
		return cached, nil
	}
	if err != nil && !errors.Is(err, pagedview.ErrMissing) {
		return cached, err
	}
	observation := DirectoryObservation{Path: dir, baseSequence: cached.Sequence, Invalidation: store.observationMark(dir), Epoch: repochange.CurrentEpoch(store.root.Path)}
	root, release, err := store.navigation.Acquire(ctx, store.root.Path)
	if err != nil {
		return observation, err
	}
	defer release()
	pin, err := store.retainGeneration(headGeneration, true)
	if err != nil {
		return observation, err
	}
	defer pin.Release()
	builder, err := newStructuralBuilder(store, pin.value)
	if err != nil {
		return observation, err
	}
	defer builder.close()
	directory, err := c.openObservationDirectory(ctx, store, root, dir)
	if err != nil {
		return observation, err
	}
	defer directory.close()
	observation.Stamp = directory.stamp
	// Unwatched membership is reusable only after its directory has been re-statted.
	if cached.Complete && cached.Failure == "" && cached.Stamp.known() && cached.Stamp == observation.Stamp && cached.Invalidation == observation.Invalidation && cached.Epoch == observation.Epoch {
		publish(cached)
		return cached, nil
	}
	reader := directoryBatch{file: directory.entries}
	for {
		finish, err := c.trees.broker.Acquire(ctx, store.observationRequest(dir))
		if err != nil {
			return observation, err
		}
		quantum := indexBatchSize
		if maximum := demand(); maximum > 0 {
			quantum = min(quantum, maximum-observation.Entries)
		}
		if quantum <= 0 {
			finish()
			break
		}
		batch, complete, readErr := reader.read(quantum)
		var nodes []indexNode
		if readErr == nil {
			nodes = readObservationNodes(root, directory, dir, batch)
		}
		finish()
		if readErr != nil {
			return observation, readErr
		}
		observation.Entries += len(batch)
		observation.Complete = complete
		if err := builder.observe(ctx, directoryDiscovery{nodes: nodes, observation: observation}); err != nil {
			return observation, err
		}
		if complete {
			break
		}
	}
	if err := store.publishStructure(ctx, builder, pin.Generation); err != nil {
		return observation, err
	}
	published, err := store.readObservation(ctx, dir)
	if err != nil {
		return observation, err
	}
	publish(published)
	return published, nil
}

func readObservationNodes(root *os.Root, directory *observationDirectory, dir string, batch []directoryEntry) []indexNode {
	nodes := make([]indexNode, 0, len(batch))
	for _, entry := range batch {
		rel := path.Join(dir, entry.Name())
		if directory.privateFilter.Contains(entry.Name()) {
			continue
		}
		physical := path.Join(directory.resolved, entry.Name())
		node := indexNode{path: rel, parent: dir, name: entry.Name(), depth: pathDepth(rel),
			isDir: entry.IsDir(), isSymlink: entry.Type()&os.ModeSymlink != 0,
			regular: entry.Type().IsRegular(), hidden: hiddenIndexPath(rel)}
		if node.isSymlink {
			targetPath, targetErr := observationPath(root, physical)
			if targetErr == nil {
				target, statErr := root.Stat(filepath.FromSlash(targetPath))
				if errors.Is(statErr, os.ErrNotExist) {
					continue
				}
				if statErr == nil {
					node.isDir = target.IsDir()
				}
			}
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func validateObservationDirectory(root Root, dir string) error {
	if filepath.IsAbs(dir) || sandbox.HasParentTraversal(dir) {
		return os.ErrPermission
	}
	if repochange.IsPrivatePath(filepath.Join(root.Path, filepath.FromSlash(dir))) {
		return os.ErrPermission
	}
	return nil
}

const DirectoryBatchLimit = 1024

type directoryDiscovery struct {
	nodes       []indexNode
	observation DirectoryObservation
}

// Foreground directories share observations independently of the recursive scanner.
func (c *Directories) ObserveDirectories(ctx context.Context, project string, root Root, dirs []string, priority backgroundwork.Priority) error {
	if len(dirs) > DirectoryBatchLimit {
		return errors.New("directory observation batch exceeds limit")
	}
	for _, dir := range dirs {
		observation, err := c.ObserveDirectory(ctx, project, root, dir, DirectoryRead{Priority: priority})
		if err != nil && observation.Failure == "" {
			return err
		}
	}
	return nil
}

// Absolute internal links resolve to a confined, root-relative spelling.
func observationPath(root *os.Root, rel string) (string, error) {
	base := fspath.CanonicalPath(root.Name())
	target := fspath.CanonicalPath(filepath.Join(root.Name(), filepath.FromSlash(rel)))
	if base == "" || target == "" || repochange.IsPrivatePath(target) {
		return "", os.ErrPermission
	}
	resolved, err := filepath.Rel(filepath.FromSlash(base), filepath.FromSlash(target))
	if err != nil || !filepath.IsLocal(resolved) {
		return "", os.ErrPermission
	}
	resolved = filepath.ToSlash(resolved)
	return resolved, nil
}

type observationDirectory struct {
	handle        *os.File
	entries       *directoryKindReader
	resolved      string
	privateFilter *repochange.PrivateDirectoryFilter
	// stamp is taken before the first entry is read, so any change during the
	// listing leaves the recorded stamp behind.
	stamp DirectoryStamp
}

func (d *observationDirectory) close() {
	if d.entries != nil && d.entries.file != d.handle {
		_ = d.entries.file.Close()
	}
	if d.handle != nil {
		_ = d.handle.Close()
	}
}

func (c *Directories) openObservationDirectory(ctx context.Context, store *indexStore, root *os.Root, dir string) (*observationDirectory, error) {
	release, err := c.trees.broker.Acquire(ctx, store.observationRequest(dir))
	if err != nil {
		return nil, err
	}
	defer release()
	return openConfinedDirectory(root, dir)
}

func openConfinedDirectory(root *os.Root, dir string) (*observationDirectory, error) {
	resolved, err := observationPath(root, dir)
	if err != nil {
		return nil, err
	}
	file, err := openDirectoryFile(root, filepath.FromSlash(resolved))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	entries, err := directoryKinds(file)
	if err != nil {
		return nil, err
	}
	return &observationDirectory{
		entries:       entries,
		resolved:      resolved,
		privateFilter: repochange.NewPrivateDirectoryFilter(filepath.Join(root.Name(), filepath.FromSlash(resolved))),
		stamp:         directoryStampOf(info),
	}, nil
}

type observationInterests struct {
	mu     sync.Mutex
	groups map[string]*backgroundwork.PriorityGroup
}

func (i *observationInterests) join(dir string, priority backgroundwork.Priority) func() {
	i.mu.Lock()
	if i.groups == nil {
		i.groups = make(map[string]*backgroundwork.PriorityGroup)
	}
	group := i.groups[dir]
	if group == nil {
		group = &backgroundwork.PriorityGroup{}
		i.groups[dir] = group
	}
	release := group.Add(priority)
	i.mu.Unlock()
	return func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		release()
		if group.Empty() && i.groups[dir] == group {
			delete(i.groups, dir)
		}
	}
}
func (s *indexStore) observationRequest(dir string) backgroundwork.Request {
	s.observationInterests.mu.Lock()
	group := s.observationInterests.groups[dir]
	s.observationInterests.mu.Unlock()
	return backgroundwork.Request{Key: s.workKey() + ":directory:" + dir, Epoch: repochange.CurrentEpoch(s.root.Path).Value,
		Lane: s.root.Path, Priority: backgroundwork.PriorityProactive, Interests: group, Resources: []backgroundwork.Resource{backgroundwork.ResourceDirectory}}
}

// A failed listing preserves known children and settles readers with a visible error.
func (s *indexStore) recordObservationFailure(ctx context.Context, failed DirectoryObservation, cause error) error {
	if failed.Path == "" {
		return nil
	}
	pin, err := s.retainGeneration(headGeneration, true)
	if err != nil {
		return err
	}
	defer pin.Release()
	builder, err := newStructuralBuilder(s, pin.value)
	if err != nil {
		return err
	}
	defer builder.close()
	index, previous, err := builder.children(ctx, failed.Path)
	if err != nil {
		return err
	}
	if previous.Sequence != failed.baseSequence {
		return errObservationChanged
	}
	failed.Sequence = structuralObservationSerial.Add(1)
	failed.FirstListed = failed.Sequence
	failed.Entries = previous.Entries
	failed.Observed = time.Now()
	message := cause.Error()
	if len(message) > 4096 {
		message = message[:4096]
	}
	failed.Failure = message
	failed.Complete = false
	if err := builder.save(ctx, index, failed); err != nil {
		return err
	}
	if err := builder.directories.UpdateFlags(ctx, failed.Path, directoryDirty|directoryRepair, 0); err != nil {
		return err
	}
	return s.publishStructure(ctx, builder, pin.Generation)
}

func DirectoryAncillary(state DirectoryState, children int64) string {
	if state.Failure != "" {
		return "error"
	}
	if !state.Complete && children == 0 {
		return "loading"
	}
	if children == 0 {
		return "empty"
	}
	return ""
}
func directoryBodyRows(state DirectoryState, children int64) int64 {
	if DirectoryAncillary(state, children) != "" {
		children++
	}
	return children
}

// An unchanged membership retains its pages and sequence while refreshing its observation.
func (b *structuralBuilder) revalidate(ctx context.Context, dir string) error {
	if b.base == nil {
		return nil
	}
	previous, found, err := b.base.directories.Get(ctx, dir)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	current, observation, err := b.children(ctx, dir)
	if err != nil {
		return err
	}
	if !observation.Complete || stateOf(previous.observation) != stateOf(observation) {
		return nil
	}
	old := &pagedview.RangeIndex[TreeItem]{Store: b.base, Root: previous.page}
	after := ""
	for {
		before, err := old.ReadAfter(ctx, after, indexBatchSize)
		if err != nil {
			return err
		}
		now, err := current.ReadAfter(ctx, after, indexBatchSize)
		if err != nil {
			return err
		}
		if len(before) != len(now) {
			return nil
		}
		for i, item := range before {
			if item.Key != now[i].Key || item.Value.Path != now[i].Value.Path || item.Value.Symlink != now[i].Value.Symlink {
				return nil
			}
		}
		if len(before) == 0 {
			break
		}
		after = before[len(before)-1].Key
	}
	observation.Sequence = previous.observation.Sequence
	observation.FirstListed = previous.observation.FirstListed
	current.Root = previous.page
	return b.save(ctx, current, observation)
}

// Explicit recursive disclosure reads only the named lazy subtree. It does not
// broaden background inventory, indexing, or watcher admission.
func (c *Directories) observeDisclosedSubtree(ctx context.Context, store *indexStore, dir string) error {
	releaseInterest := store.observationInterests.join(dir, backgroundwork.PriorityInteractive)
	defer releaseInterest()
	root, release, err := store.navigation.Acquire(ctx, store.root.Path)
	if err != nil {
		return err
	}
	defer release()
	pin, err := store.retainGeneration(headGeneration, true)
	if err != nil {
		return err
	}
	defer pin.Release()
	builder, err := newStructuralBuilder(store, pin.value)
	if err != nil {
		return err
	}
	defer builder.close()
	options := structuralScanOptions{store: store, explicitRoot: dir, broker: c.trees.broker, epoch: repochange.CurrentEpoch(store.root.Path)}
	options.request = func(path string) backgroundwork.Request {
		request := store.observationRequest(path)
		request.Priority = backgroundwork.PriorityInteractive
		return request
	}
	if err := scanStructureRoot(ctx, root, dir, options, func(listing directoryDiscovery) error {
		if listing.observation.Failure != "" {
			return builder.retainFailedDirectory(ctx, listing.observation)
		}
		return builder.observe(ctx, listing)
	}); err != nil {
		return err
	}
	return store.publishStructure(ctx, builder, pin.Generation)
}
