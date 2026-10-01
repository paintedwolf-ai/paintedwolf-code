package sourcecatalog

import (
	"context"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// Directory keys precede file keys, so entire file-only branches need no reads.
func visitStructuralDirectoryEdges(ctx context.Context, store pagedview.PageStore[TreeItem], root uint64, visit func(pagedview.RangeItem[TreeItem]) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if root == 0 {
		return nil
	}
	page, err := store.Read(ctx, root)
	if err != nil {
		return err
	}
	for _, item := range page.Items {
		if !directoryOrderKind(item.Key) {
			break
		}
		if err := visit(item); err != nil {
			return err
		}
	}
	for _, child := range page.Children {
		if !directoryOrderKind(child.Key) {
			break
		}
		if err := visitStructuralDirectoryEdges(ctx, store, child.Page, visit); err != nil {
			return err
		}
	}
	return nil
}
