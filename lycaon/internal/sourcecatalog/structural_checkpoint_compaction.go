package sourcecatalog

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lycaon/lycaon/internal/pagedview"
)

const (
	structuralCompactionDebtBytes = 256 << 20
	structuralCompactionDebtRatio = 4
	// Each publication adds a segment its successors inherit, and a spilled one
	// holds a descriptor while any generation names it. Debt cannot see that:
	// small deltas stay under the floor while lengthening every page read.
	structuralCompactionSegments = 128
	// Directories a compaction absorbs after its copy. Past this the root is
	// moving faster than the copy runs, and the next checkpoint starts fresh.
	structuralCompactionCatchUp = 8192
)

// structuralCompactionDue reports whether a generation has earned a rewrite.
func structuralCompactionDue(g *structuralGeneration) bool {
	if len(g.segments) >= structuralCompactionSegments {
		return true
	}
	return g.compactionDebtBytes >= structuralCompactionDebtBytes &&
		g.compactionDebtBytes >= g.encodedBytes/structuralCompactionDebtRatio
}

// compactCheckpointPin rewrites the pinned generation into fresh segments.
// Copying a tree outlasts the interval between publications, so the copy runs
// unfenced and the publication gate covers only the catch-up and the install.
func (s *indexStore) compactCheckpointPin(ctx context.Context, pin *GenerationPin) (*GenerationPin, error) {
	if !structuralCompactionDue(pin.value) {
		return nil, nil
	}
	if pin.Generation >= headGeneration-1 {
		return nil, pagedview.ErrBudget
	}
	work, err := newStructuralCompaction(ctx, s, pin.value)
	if err != nil {
		return nil, err
	}
	defer work.close()
	releasePublication, err := s.acquireStructurePublication(ctx)
	if err != nil {
		return nil, err
	}
	defer releasePublication()
	caught, err := work.catchUp(ctx)
	if err != nil {
		return nil, err
	}
	if !caught {
		// The root moved wider than one catch-up absorbs. The next checkpoint
		// compacts from a newer generation rather than holding publication open.
		slog.DebugContext(ctx, "structural compaction outrun", "root", s.root.ID, "generation", pin.Generation)
		return nil, nil
	}
	source := work.source
	compacted, err := work.seal(ctx)
	if err != nil {
		return nil, err
	}
	compacted.id = source.id + 1
	return s.installCompaction(ctx, source, compacted)
}

// installCompaction publishes a compacted generation under the publication gate
// the caller already holds, so the head it was caught up to is still head.
func (s *indexStore) installCompaction(ctx context.Context, expected, compacted *structuralGeneration) (*GenerationPin, error) {
	s.mu.Lock()
	if s.structure != expected || s.checkpoint.Drained() || s.pins.drained || ctx.Err() != nil {
		s.mu.Unlock()
		compacted.close()
		return nil, ctx.Err()
	}
	s.installStructureLocked(compacted)
	s.pins.held[compacted.id] = &generationPin{count: 1, value: compacted}
	replacement := &GenerationPin{store: s, Generation: compacted.id, value: compacted}
	s.mu.Unlock()
	s.stores.Directories.navigationChanged(s.root)
	return replacement, nil
}

// structuralCompaction is a whole-tree copy that absorbs later publications
// before it lands. Its builder has no base, so the sealed generation names only
// segments it wrote itself.
type structuralCompaction struct {
	store     *indexStore
	builder   *structuralBuilder
	workspace *structuralCheckpointWorkspace
	pin       *GenerationPin
	source    *structuralGeneration
}

// newStructuralCompaction copies source. The caller keeps source alive for the
// copy; a later catch-up takes its own pin on whatever it advances to.
func newStructuralCompaction(ctx context.Context, store *indexStore, source *structuralGeneration) (*structuralCompaction, error) {
	work := &structuralCompaction{store: store, source: source}
	ready := false
	var err error
	defer func() {
		if !ready {
			work.close()
		}
	}()
	if work.builder, err = newStructuralBuilder(store, nil); err != nil {
		return nil, err
	}
	if work.workspace, err = newStructuralCheckpointWorkspace(ctx); err != nil {
		return nil, err
	}
	copier := structuralGenerationCopy{builder: work.builder, workspace: work.workspace}
	err = work.source.directories.Visit(ctx, func(directory structuralDirectory) error {
		page, err := copier.page(ctx, work.source, directory.page, 0)
		if err != nil {
			return err
		}
		directory.page = page
		return work.builder.directories.Set(ctx, directory.observation.Path, directory)
	})
	if err != nil {
		return nil, err
	}
	ready = true
	return work, nil
}

// catchUp copies directories that changed between the copied generation and
// head into the same fresh segments. Page ids are unique across the process, so
// the copy memo carries over and only genuinely new pages are written.
func (w *structuralCompaction) catchUp(ctx context.Context) (bool, error) {
	current, err := w.store.retainGeneration(headGeneration, true)
	if err != nil {
		return false, err
	}
	release := true
	defer func() {
		if release {
			current.Release()
		}
	}()
	if current.value == w.source {
		return true, nil
	}
	copier := structuralGenerationCopy{builder: w.builder, workspace: w.workspace}
	err = visitDirectoryIndexDiff(ctx, w.source.directories, current.value.directories, structuralCompactionCatchUp,
		func(key string, _ bool, _ structuralDirectory, newFound bool, value structuralDirectory) error {
			if !newFound {
				if err := w.builder.directories.DeleteSubtree(ctx, key); err != nil && !errors.Is(err, pagedview.ErrMissing) {
					return err
				}
				return nil
			}
			page, err := copier.page(ctx, current.value, value.page, 0)
			if err != nil {
				return err
			}
			value.page = page
			return w.builder.directories.Set(ctx, key, value)
		})
	if errors.Is(err, errStructuralMergeUnavailable) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if w.pin != nil {
		w.pin.Release()
	}
	w.pin, w.source = current, current.value
	release = false
	return true, nil
}

func (w *structuralCompaction) seal(ctx context.Context) (*structuralGeneration, error) {
	return w.builder.seal(ctx, w.source.id)
}

func (w *structuralCompaction) close() {
	if w.builder != nil {
		w.builder.close()
		w.builder = nil
	}
	if w.workspace != nil {
		_ = w.workspace.close()
		w.workspace = nil
	}
	if w.pin != nil {
		w.pin.Release()
		w.pin = nil
	}
}

type structuralGenerationCopy struct {
	builder   *structuralBuilder
	workspace *structuralCheckpointWorkspace
}

func (copy *structuralGenerationCopy) page(ctx context.Context, from *structuralGeneration, id uint64, depth int) (uint64, error) {
	if id == 0 {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	result, found, err := copy.workspace.beginCopy(ctx, id)
	if err != nil || found {
		return result, err
	}
	if depth > 64 {
		return 0, pagedview.ErrRange
	}
	page, err := from.Read(ctx, id)
	if err != nil {
		return 0, err
	}
	for i := range page.Children {
		page.Children[i].Page, err = copy.page(ctx, from, page.Children[i].Page, depth+1)
		if err != nil {
			return 0, err
		}
	}
	result, err = copy.builder.Write(ctx, 0, page)
	if err != nil {
		return 0, err
	}
	if err := copy.workspace.finishCopy(ctx, id, result); err != nil {
		return 0, err
	}
	return result, nil
}
