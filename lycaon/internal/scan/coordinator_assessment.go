package scan

import (
	"context"
	"fmt"
	"slices"
)

// A cadence assessment covers the union of its scanners' deltas. Each member
// retains its own target and trigger without redefining that immutable group.
func (c *CoordinatorImpl) ensureEnqueueAssessment(ctx context.Context, req EnqueueRequest, member AssessmentDraft) error {
	draft := member
	if req.Assessment != nil {
		draft = *req.Assessment
		if draft.ID != member.ID || draft.CanonicalPath != member.CanonicalPath ||
			draft.SourceSnapshotID != member.SourceSnapshotID || draft.Target.Kind != member.Target.Kind ||
			!slices.Equal(UniqueSortedStrings(draft.RequiredScanners), UniqueSortedStrings(member.RequiredScanners)) ||
			!slices.Contains(draft.RequiredScanners, req.ScannerID) ||
			!assessmentIncludesPaths(draft.Target.Paths, member.Target.Paths) ||
			!assessmentIncludesPaths(draft.Target.DeletedPaths, member.Target.DeletedPaths) {
			return fmt.Errorf("%w: %s member %s", ErrAssessmentIdentityMismatch, draft.ID, req.ScannerID)
		}
	}
	_, err := c.Store.EnsureAssessment(ctx, draft)
	return err
}

func assessmentIncludesPaths(assessment, member []string) bool {
	allowed := NormalizeScanPaths(assessment)
	for _, path := range NormalizeScanPaths(member) {
		if _, found := slices.BinarySearch(allowed, path); !found {
			return false
		}
	}
	return true
}
