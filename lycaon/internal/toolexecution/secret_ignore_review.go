package toolexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectignore"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

func (e *Secrets) SetSecretIgnores(s *projectignore.SecretService) { e.secretIgnores = s }

func (e *Secrets) offerSecretIgnoreReview(ctx context.Context, finding secretmatch.Alert, payload *hitl.SecretScreen) func() {
	unavailable := func() {}
	if e.secretIgnores == nil || e.secretIgnores.Roots == nil || finding.Managed() || len(finding.Fingerprints) != 1 {
		return unavailable
	}
	if !e.secretMatcher.ReviewMatches(ctx, finding.ReviewValue, finding.Fingerprints[0]) {
		return unavailable
	}
	if e.secretIgnores.Trusted != nil && !e.secretIgnores.Trusted(ctx, finding.ProjectID) {
		return unavailable
	}
	roots, err := e.secretIgnores.Roots(ctx, finding.ProjectID)
	if err != nil || len(roots) == 0 {
		return unavailable
	}
	id, release := e.secretIgnores.Reviews.Offer(ctx, finding.ProjectID, finding.ReviewValue)
	payload.IgnoreCandidateID = id
	return release
}
