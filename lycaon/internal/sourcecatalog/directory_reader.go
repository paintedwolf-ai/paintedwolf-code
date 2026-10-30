package sourcecatalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/pagedview"
)

const foregroundDirectoryBatch = 64
const foregroundEntryBatch = 4096

// DirectoryReadAdmission re-admits a borrowed directory at its current address.
type DirectoryReadAdmission interface {
	DirectoryFile() *os.File
	AdmitDirectoryRead() error
}

// DirectoryReader consumes fresh observations before publishing their structural
// projection. Close drains its bounded pending batch; it starts no background work.
// A reader belongs to one traversal and is not safe for concurrent calls.
type DirectoryReader struct {
	directories *Directories
	project     string
	root        Root
	store       *indexStore
	builder     *structuralBuilder
	pin         *GenerationPin
	pending     map[string]os.FileInfo
	entries     int
	closed      bool
}

func (c *Directories) NewDirectoryReader(project string, root Root) *DirectoryReader {
	return &DirectoryReader{directories: c, project: project, root: root}
}

// ReadDirectory borrows a directory capability for dir when supplied.
// Rewind its consumed offset before falling back to another listing reader.
func (r *DirectoryReader) ReadDirectory(ctx context.Context, dir string, maximum int, held DirectoryReadAdmission) (DirectoryListing, error) {
	if r.closed {
		return DirectoryListing{}, os.ErrClosed
	}
	if err := validateObservationDirectory(r.root, dir); err != nil {
		return DirectoryListing{}, err
	}
	if err := ctx.Err(); err != nil {
		return DirectoryListing{}, err
	}
	var err error
	if r.store == nil {
		r.store, err = r.directories.trees.indexStore(ctx, r.project, r.root)
		if err != nil {
			return DirectoryListing{}, err
		}
	}
	dir = normalizeDir(dir)
	if _, ok := r.pending[dir]; ok {
		if err := r.flush(ctx); err != nil {
			return DirectoryListing{}, err
		}
	}
	cached, err := r.store.readObservation(ctx, dir)
	if err != nil && !errors.Is(err, pagedview.ErrMissing) {
		return DirectoryListing{}, err
	}
	// Watched freshness needs no live scan. Unwatched directories are checked
	// against the supplied descriptor's stamp by the observation collector.
	if held == nil && r.store.policy.boundaryPath(dir, true) == "" && r.store.observationFresh(cached) {
		return readDirectoryNamespace(ctx, r.store, dir, cached, maximum, true)
	}
	limit := 0
	if maximum > 0 {
		limit = maximum + 1
	}
	if r.pin == nil {
		r.pin, err = r.store.retainGeneration(headGeneration, true)
		if err != nil {
			return DirectoryListing{}, err
		}
	}
	listing := DirectoryListing{Prepared: true}
	release := r.store.observationInterests.join(dir, backgroundwork.PriorityInteractive)
	defer release()
	observation, reused, err := collectDirectoryObservation(ctx, r.store, dir, cached, held, func() int { return limit }, func(discovery directoryDiscovery) error {
		if err := r.begin(ctx); err != nil {
			return err
		}
		if err := r.builder.observe(ctx, discovery); err != nil {
			return err
		}
		for _, node := range discovery.nodes {
			listing.Entries = append(listing.Entries, Entry{RootID: r.root.ID, Path: node.path, Parent: node.parent, Name: node.name, Depth: node.depth, IsDir: node.isDir, IsSymlink: node.isSymlink, Boundary: r.store.policy.boundaryDir(node.path) != ""})
		}
		return nil
	})
	if err != nil {
		r.discard()
		if ctx.Err() == nil && !errors.Is(err, os.ErrInvalid) && !errors.Is(err, errObservationChanged) && !errors.Is(err, backgroundwork.ErrSuperseded) {
			err = errors.Join(err, r.store.recordObservationFailure(ctx, observation, err))
		}
		return DirectoryListing{Prepared: true}, err
	}
	if reused {
		if r.builder == nil {
			r.discard()
		}
		return readDirectoryNamespace(ctx, r.store, dir, observation, maximum, true)
	}
	var identity os.FileInfo
	if held != nil {
		identity, err = held.DirectoryFile().Stat()
		if err != nil {
			r.discard()
			return DirectoryListing{Prepared: true}, err
		}
	}
	r.pending[dir] = identity
	r.entries += observation.Entries
	if !observation.Complete || len(listing.Entries) != observation.Entries || maximum > 0 && observation.Entries > maximum {
		listing.Entries = nil
		return listing, errors.Join(ErrDirectoryNamespace, r.flush(ctx))
	}
	if len(r.pending) >= foregroundDirectoryBatch || r.entries >= foregroundEntryBatch {
		if err := r.flush(ctx); err != nil {
			return DirectoryListing{Prepared: true}, err
		}
	}
	sort.Slice(listing.Entries, func(i, j int) bool { return listing.Entries[i].Name < listing.Entries[j].Name })
	return listing, nil
}

func (r *DirectoryReader) begin(ctx context.Context) error {
	if r.builder != nil {
		return nil
	}
	builder, err := newStructuralBuilder(r.store, r.pin.value)
	if err != nil {
		return err
	}
	r.builder = builder
	r.pending = make(map[string]os.FileInfo)
	return nil
}

func (r *DirectoryReader) flush(ctx context.Context) error {
	if r.builder == nil {
		r.discard()
		return nil
	}
	defer r.discard()
	// A descriptor can outlive its path after rename. Such an observation remains
	// useful to the foreground caller but cannot describe the current catalog path.
	root, release, err := acquireObservationRoot(ctx, r.store)
	if err != nil {
		return err
	}
	defer release()
	for dir, info := range r.pending {
		if info == nil {
			continue
		}
		current, err := root.Stat(filepath.FromSlash(dir))
		if err != nil || !os.SameFile(info, current) {
			return errObservationChanged
		}
	}
	return r.store.publishStructure(ctx, r.builder, r.pin.Generation)
}

func (r *DirectoryReader) discard() {
	if r.builder != nil {
		r.builder.close()
		r.builder = nil
	}
	if r.pin != nil {
		r.pin.Release()
		r.pin = nil
	}
	r.pending = nil
	r.entries = 0
}

func (r *DirectoryReader) Close(ctx context.Context) error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.flush(ctx)
}
