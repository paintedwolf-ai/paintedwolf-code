package visual

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// ResolveRef resolves a session-tree artifact UUID or evidence handle to a store
// id. Handles match VisualArtifact.EvidenceHandle under the tree root. The id is
// non-empty exactly when the resolution is present.
func ResolveRef(ctx context.Context, store Store, rootSessionID, ref string) (artifactID string, res Resolution) {
	ref = strings.TrimSpace(ref)
	root := normalizeKey(rootSessionID)
	if store == nil || root == "" || ref == "" {
		return "", Absent(AbsenceUnknown)
	}
	direct := store.Resolve(ctx, root, ref)
	if direct.IsPresent() {
		return normalizeID(ref), direct
	}
	if direct.Reason() == AbsenceForeign || !evidence.IsEvidenceHandleToken(ref) {
		return "", direct
	}
	return findByEvidenceHandle(ctx, store, root, ref)
}

// findByEvidenceHandle asks the store which artifact holds the handle, so the
// durable tier answers with one indexed query.
func findByEvidenceHandle(ctx context.Context, store Store, root, handle string) (string, Resolution) {
	id, err := store.FindByEvidenceHandle(ctx, root, strings.TrimSpace(handle))
	if err != nil {
		return "", Absent(AbsenceUnknown)
	}
	if id = normalizeID(id); id == "" {
		return "", Absent(AbsenceUnknown)
	}
	res := store.Resolve(ctx, root, id)
	if !res.IsPresent() {
		return "", res
	}
	return id, res
}
