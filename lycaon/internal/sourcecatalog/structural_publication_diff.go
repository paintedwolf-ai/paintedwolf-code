package sourcecatalog

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/pagedview"
)

var errStructuralMergeUnavailable = errors.New("structural merge unavailable")

func visitDirectoryIndexDiff(ctx context.Context, oldIndex, newIndex structuralDirectoryIndex, limit int, visit func(string, bool, structuralDirectory, bool, structuralDirectory) error) error {
	remaining := limit
	changed := func(key string, oldFound bool, oldValue structuralDirectory, newFound bool, newValue structuralDirectory) error {
		if remaining <= 0 {
			return errStructuralMergeUnavailable
		}
		remaining--
		return visit(key, oldFound, oldValue, newFound, newValue)
	}
	return diffDirectoryPages(ctx, oldIndex.store, oldIndex.root, newIndex.store, newIndex.root, changed)
}

func diffDirectoryPages(ctx context.Context, oldStore pagedview.PageStore[structuralDirectory], oldPage uint64, newStore pagedview.PageStore[structuralDirectory], newPage uint64, visit func(string, bool, structuralDirectory, bool, structuralDirectory) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if oldPage == newPage {
		return nil
	}
	oldContent, oldFound, err := readDirectoryPageForDiff(ctx, oldStore, oldPage)
	if err != nil {
		return err
	}
	newContent, newFound, err := readDirectoryPageForDiff(ctx, newStore, newPage)
	if err != nil {
		return err
	}
	if !oldFound {
		return visitDirectoryPageSubtree(ctx, newStore, newPage, false, true, visit)
	}
	if !newFound {
		return visitDirectoryPageSubtree(ctx, oldStore, oldPage, true, false, visit)
	}
	if len(oldContent.Children) == 0 && len(newContent.Children) == 0 {
		return diffDirectoryItems(oldContent.Items, newContent.Items, visit)
	}
	if len(oldContent.Children) == 0 || len(newContent.Children) == 0 || len(oldContent.Children) != len(newContent.Children) {
		return errStructuralMergeUnavailable
	}
	for index := range oldContent.Children {
		if oldContent.Children[index].Key != newContent.Children[index].Key {
			return errStructuralMergeUnavailable
		}
	}
	for index := range oldContent.Children {
		if err := diffDirectoryPages(ctx, oldStore, oldContent.Children[index].Page, newStore, newContent.Children[index].Page, visit); err != nil {
			return err
		}
	}
	return nil
}

func readDirectoryPageForDiff(ctx context.Context, store pagedview.PageStore[structuralDirectory], id uint64) (pagedview.RangePage[structuralDirectory], bool, error) {
	if id == 0 {
		return pagedview.RangePage[structuralDirectory]{}, false, nil
	}
	page, err := store.Read(ctx, id)
	return page, err == nil, err
}

func diffDirectoryItems(oldItems, newItems []pagedview.RangeItem[structuralDirectory], visit func(string, bool, structuralDirectory, bool, structuralDirectory) error) error {
	oldAt, newAt := 0, 0
	for oldAt < len(oldItems) || newAt < len(newItems) {
		switch {
		case newAt == len(newItems):
			item := oldItems[oldAt]
			if err := visit(item.Key, true, item.Value, false, structuralDirectory{}); err != nil {
				return err
			}
			oldAt++
		case oldAt == len(oldItems):
			item := newItems[newAt]
			if err := visit(item.Key, false, structuralDirectory{}, true, item.Value); err != nil {
				return err
			}
			newAt++
		case oldItems[oldAt].Key == newItems[newAt].Key:
			if oldItems[oldAt].Value.observation != newItems[newAt].Value.observation {
				if err := visit(oldItems[oldAt].Key, true, oldItems[oldAt].Value, true, newItems[newAt].Value); err != nil {
					return err
				}
			}
			oldAt++
			newAt++
		case oldItems[oldAt].Key < newItems[newAt].Key:
			item := oldItems[oldAt]
			if err := visit(item.Key, true, item.Value, false, structuralDirectory{}); err != nil {
				return err
			}
			oldAt++
		default:
			item := newItems[newAt]
			if err := visit(item.Key, false, structuralDirectory{}, true, item.Value); err != nil {
				return err
			}
			newAt++
		}
	}
	return nil
}

func visitDirectoryPageSubtree(ctx context.Context, store pagedview.PageStore[structuralDirectory], id uint64, oldFound, newFound bool, visit func(string, bool, structuralDirectory, bool, structuralDirectory) error) error {
	if id == 0 {
		return nil
	}
	page, err := store.Read(ctx, id)
	if err != nil {
		return err
	}
	if len(page.Children) == 0 {
		for _, item := range page.Items {
			if err := visit(item.Key, oldFound, item.Value, newFound, item.Value); err != nil {
				return err
			}
		}
		return nil
	}
	for _, child := range page.Children {
		if err := visitDirectoryPageSubtree(ctx, store, child.Page, oldFound, newFound, visit); err != nil {
			return err
		}
	}
	return nil
}
