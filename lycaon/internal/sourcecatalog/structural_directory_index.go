package sourcecatalog

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/pagedview"
)

type structuralDirectory struct {
	page        uint64
	observation DirectoryObservation
}

// Directory metadata shares the immutable segment lifetime of the file ranges.
type structuralDirectoryIndex struct {
	root  uint64
	count int
	store pagedview.PageStore[structuralDirectory]
}

func (index structuralDirectoryIndex) Len() int { return index.count }
func (index structuralDirectoryIndex) Get(ctx context.Context, key string) (structuralDirectory, bool, error) {
	if err := ctx.Err(); err != nil {
		return structuralDirectory{}, false, err
	}
	if index.root == 0 {
		return structuralDirectory{}, false, nil
	}
	tree := pagedview.RangeIndex[structuralDirectory]{Store: index.store, Root: index.root}
	item, _, err := tree.LocateItem(ctx, key)
	if errors.Is(err, pagedview.ErrMissing) {
		return structuralDirectory{}, false, nil
	}
	return item.Value, err == nil, err
}
func (index structuralDirectoryIndex) Visit(ctx context.Context, visit func(structuralDirectory) error) error {
	return index.visitAfter(ctx, "", "", visit)
}

// VisitChildren skips descendant key ranges without retaining directory names.
func (index structuralDirectoryIndex) VisitChildren(ctx context.Context, parent string, visit func(structuralDirectory) error) error {
	prefix := ""
	if parent != "." {
		prefix = parent + "/"
	}
	after, inclusive := prefix, false
	tree := pagedview.RangeIndex[structuralDirectory]{Store: index.store, Root: index.root}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A slash-prefix upper bound can itself name an immediate child.
		if inclusive {
			value, found, err := index.Get(ctx, after)
			if err != nil {
				return err
			}
			if found {
				if err := visit(value); err != nil {
					return err
				}
			}
			inclusive = false
		}
		items, err := tree.ReadAfter(ctx, after, indexBatchSize)
		if err != nil || len(items) == 0 {
			return err
		}
		for _, item := range items {
			if !strings.HasPrefix(item.Key, prefix) {
				return nil
			}
			after = item.Key
			if item.Key == "." {
				continue
			}
			if slash := strings.IndexByte(item.Key[len(prefix):], '/'); slash >= 0 {
				after = item.Key[:len(prefix)+slash] + "0"
				inclusive = true
				break
			}
			if err := visit(item.Value); err != nil {
				return err
			}
		}
	}
}

func (index structuralDirectoryIndex) visitAfter(ctx context.Context, after, prefix string, visit func(structuralDirectory) error) error {
	tree := pagedview.RangeIndex[structuralDirectory]{Store: index.store, Root: index.root}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		items, err := tree.ReadAfter(ctx, after, indexBatchSize)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			if prefix != "" && !strings.HasPrefix(item.Key, prefix) {
				return nil
			}
			if err := visit(item.Value); err != nil {
				return err
			}
		}
		after = items[len(items)-1].Key
	}
}
