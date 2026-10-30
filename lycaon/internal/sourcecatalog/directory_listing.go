package sourcecatalog

import (
	"context"
	"errors"
	"sort"

	"github.com/lycaon/lycaon/internal/backgroundwork"
)

// ErrDirectoryNamespace identifies a listing that omits filesystem entries.
var ErrDirectoryNamespace = errors.New("directory namespace is not complete")

// DirectoryListing contains the complete immediate namespace in byte order.
type DirectoryListing struct {
	Entries []Entry
	Reused  bool
}

// ReadDirectory reuses fresh membership and prepares cold directories on demand.
// Read admission and live metadata remain the caller's responsibility.
func (c *Directories) ReadDirectory(ctx context.Context, project string, root Root, dir string) (DirectoryListing, error) {
	if err := validateObservationDirectory(root, dir); err != nil {
		return DirectoryListing{}, err
	}
	store, err := c.trees.indexStore(ctx, project, root)
	if err != nil {
		return DirectoryListing{}, err
	}
	dir = normalizeDir(dir)
	previous, _ := store.readObservation(ctx, dir)
	observation, err := c.observeUntil(ctx, store, dir, 0, backgroundwork.PriorityInteractive)
	if err != nil {
		return DirectoryListing{}, err
	}
	if !observation.Complete || observation.Failure != "" {
		return DirectoryListing{}, ErrDirectoryNamespace
	}
	navigation, err := openNavigation(ctx, store, headGeneration)
	if err != nil {
		return DirectoryListing{}, err
	}
	defer func() { _ = navigation.Close() }()
	record, found, err := navigation.pages.directories.Get(ctx, dir)
	if err != nil {
		return DirectoryListing{}, err
	}
	if !found || record.observation.Sequence != observation.Sequence {
		return DirectoryListing{}, ErrDirectoryNamespace
	}
	children, err := navigation.Children(ctx, dir)
	if err != nil {
		return DirectoryListing{}, err
	}
	listing := DirectoryListing{Reused: previous.Complete && previous.Sequence == observation.Sequence}
	after := ""
	for {
		items, err := children.ReadAfter(ctx, after, indexBatchSize)
		if err != nil {
			return DirectoryListing{}, err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			listing.Entries = append(listing.Entries, navigation.entryOf(item))
		}
		after = items[len(items)-1].Key
	}
	// Private entries and unresolved links can be absent from structural views.
	if len(listing.Entries) != observation.Entries {
		return DirectoryListing{}, ErrDirectoryNamespace
	}
	sort.Slice(listing.Entries, func(i, j int) bool { return listing.Entries[i].Name < listing.Entries[j].Name })
	return listing, nil
}
