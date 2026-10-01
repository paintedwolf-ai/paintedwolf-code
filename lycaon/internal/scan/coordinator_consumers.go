package scan

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func (c *CoordinatorImpl) reuseDedup(
	ctx context.Context,
	delegationID, canonicalPath, queueIdentity, scannerID string,
	categories []api.ScanCategory,
	paths []string,
	reuseKey, supersedes, assessmentID, workflowRunID, sessionID string,
) (*api.CodeScan, error) {
	existing, err := c.findDedup(ctx, delegationID, canonicalPath, queueIdentity, scannerID, categories, paths, reuseKey, supersedes)
	if err != nil || existing == nil {
		return existing, err
	}
	return c.bindContexts(ctx, existing, assessmentID, workflowRunID, sessionID)
}

func (c *CoordinatorImpl) bindContexts(ctx context.Context, rec *api.CodeScan, assessmentID, workflowRunID, sessionID string) (*api.CodeScan, error) {
	if rec == nil {
		return nil, nil
	}
	assessmentID = strings.TrimSpace(assessmentID)
	workflowRunID = strings.TrimSpace(workflowRunID)
	sessionID = strings.TrimSpace(sessionID)
	if assessmentID == "" && workflowRunID == "" && sessionID == "" {
		return rec, nil
	}
	return c.Store.BindContexts(ctx, rec.ID, assessmentID, workflowRunID, sessionID)
}

func projectScanContexts(rec *api.CodeScan, assessmentID, workflowRunID, sessionID string) *api.CodeScan {
	if rec == nil {
		return nil
	}
	rec.AssessmentID = strings.TrimSpace(assessmentID)
	rec.WorkflowRunID = strings.TrimSpace(workflowRunID)
	rec.SessionID = strings.TrimSpace(sessionID)
	return rec
}
