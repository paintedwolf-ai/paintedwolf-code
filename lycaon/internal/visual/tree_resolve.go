package visual

import (
	"context"
)

// ResolveInTree resolves one artifact inside a session tree.
func ResolveInTree(ctx context.Context, store Store, rootSessionID, artifactID string) Resolution {
	if store == nil {
		return Absent(AbsenceUnknown)
	}
	root := normalizeKey(rootSessionID)
	id := normalizeID(artifactID)
	if root == "" || id == "" {
		return Absent(AbsenceUnknown)
	}
	return store.Resolve(ctx, root, id)
}
