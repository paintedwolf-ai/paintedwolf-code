package sourcecatalog

import (
	"context"
	"github.com/lycaon/lycaon/internal/pagedview"
	"strings"
)

func DirectoryOrder(name string, isDir bool) string {
	kind := "1"
	if isDir {
		kind = "0"
	}
	return kind + strings.ToLower(name) + "\x00" + name
}

// directoryOrderKind recovers the kind a key was ordered by.
func directoryOrderKind(key string) (isDir bool) { return strings.HasPrefix(key, "0") }

func directoryUnresolved(ctx context.Context, children *pagedview.RangeIndex[TreeItem], state DirectoryState) (int64, error) {
	if state.Failure != "" {
		return 0, nil
	}
	pending, err := children.Unresolved(ctx)
	if !state.Complete && state.Failure == "" {
		pending++
	}
	return pending, err
}
