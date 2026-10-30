package sourcecatalog

import (
	"context"
	"errors"
	"sort"
)

// ErrDirectoryNamespace marks when a bounded complete namespace is unavailable.
var ErrDirectoryNamespace = errors.New("directory namespace requires live listing")

// DirectoryListing contains the complete immediate namespace in byte order.
type DirectoryListing struct {
	Entries  []Entry
	Reused   bool
	Prepared bool
}

func readDirectoryNamespace(ctx context.Context, store *indexStore, dir string, observation DirectoryObservation, maximum int, reused bool) (DirectoryListing, error) {
	listing := DirectoryListing{Reused: reused, Prepared: !reused}
	if !observation.Complete || observation.Failure != "" {
		return listing, ErrDirectoryNamespace
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
		return listing, ErrDirectoryNamespace
	}
	children, err := navigation.Children(ctx, dir)
	if err != nil {
		return DirectoryListing{}, err
	}
	count, err := children.Count(ctx)
	if err != nil {
		return listing, err
	}
	if count != int64(observation.Entries) {
		return listing, ErrDirectoryNamespace
	}
	if maximum > 0 && count > int64(maximum) {
		return listing, ErrDirectoryNamespace
	}
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
		listing.Entries = nil
		return listing, ErrDirectoryNamespace
	}
	sort.Slice(listing.Entries, func(i, j int) bool { return listing.Entries[i].Name < listing.Entries[j].Name })
	return listing, nil
}
