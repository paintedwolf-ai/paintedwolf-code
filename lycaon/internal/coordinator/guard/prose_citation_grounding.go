package guard

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProjectCoordinatorCloseoutProse extracts narrative on prose-closeout surfaces.
func ProjectCoordinatorCloseoutProse(surfaceID, content string) (string, bool) {
	if !SurfaceFinishesWithUserProse(surfaceID) {
		return "", false
	}
	return guidance.CoordinatorCloseoutTranscriptNarrative(content)
}

// BuildCoordinatorProseCitationGrounding records typed closeout checks on commit.
func BuildCoordinatorProseCitationGrounding(
	ctx context.Context,
	ledger guidance.CloseoutEvidenceReader,
	sess *api.Session,
	history []api.Message,
	content, surfaceID string,
	roots evidence.CitationRoots,
) (*api.CitationGrounding, error) {
	if sess == nil || sess.IsWorkerChild() || strings.TrimSpace(content) == "" || ledger == nil {
		return nil, nil
	}
	if !SurfaceFinishesWithUserProse(surfaceID) {
		return nil, nil
	}
	if strings.TrimSpace(roots.ProjectDir) == "" && len(roots.Roots) == 0 {
		return nil, nil
	}
	report, ok := guidance.ParseCoordinatorCompletionReport(content)
	if !ok {
		return nil, nil
	}
	ev, err := guidance.UnionCloseoutEvidence(ctx, ledger, sess.ID, history)
	if err != nil {
		return nil, err
	}
	eval := guidance.EvaluateCloseoutCitations(roots, surfaceID, report, ev)
	return guidance.BuildCloseoutCitationGrounding(roots, surfaceID, report, ev, eval), nil
}
