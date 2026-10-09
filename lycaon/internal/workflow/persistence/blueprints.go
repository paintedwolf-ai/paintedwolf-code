package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/pkg/api"
)

type Blueprints struct {
	transactions *Transactions
}

func (s *Blueprints) CommitBlueprintApproval(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any, digest, channel string) error {
	// The session identifies the grant throughout its lifetime.
	if run == nil || strings.TrimSpace(run.ProjectID) == "" || strings.TrimSpace(run.BlueprintPath) == "" ||
		strings.TrimSpace(digest) == "" || strings.TrimSpace(run.SessionID) == "" {
		return fmt.Errorf("canonical blueprint approval is incomplete")
	}
	// Validate the seal before issuing the grant.
	if s.transactions.authz == nil {
		return authzledger.ErrSealFailed
	}
	approver, err := people.Deciding(ctx)
	if err != nil {
		return fmt.Errorf("blueprint approver: %w", err)
	}
	raw, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	queries := db.New(tx)
	n, err := queries.UpdateWorkflowRunVars(ctx, db.UpdateWorkflowRunVarsParams{
		ProjectDir: projectDir, VarsJson: string(raw), UpdatedAt: db.FormatTime(now), ID: run.ID, Revision: run.Revision,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.transactions.revisionConflict(ctx, run.ID, run.Revision)
	}
	if err := queries.UpsertBlueprintApproval(ctx, db.UpsertBlueprintApprovalParams{
		ProjectID: run.ProjectID, Path: run.BlueprintPath, ContentDigest: digest,
		WorkflowRunID: db.NullString(run.ID), WorkflowRevision: run.Revision,
		ApprovedAt: db.FormatTime(now), ApprovedVia: strings.TrimSpace(channel), ApprovedByPersonID: approver.ID,
		SessionID: strings.TrimSpace(run.SessionID),
	}); err != nil {
		return err
	}
	if err := s.transactions.authz.AppendHumanGateTx(ctx, tx, authzledger.HumanGateRecord{
		SessionID:        run.SessionID,
		Action:           authzledger.ActionBlueprintApproved,
		Outcome:          authzledger.OutcomeAllowed,
		ResolvedBy:       authzledger.ResolvedByHuman,
		ResolverPersonID: approver.ID,
		Files:            []string{run.BlueprintPath},
		ProjectDir:       projectDir,
		BlueprintDigest:  digest,
	}); err != nil {
		return err
	}
	next := *run
	next.Revision++
	next.UpdatedAt = now
	if err := s.transactions.enqueueRunTx(ctx, tx, &next, api.WorkflowEventKindRunUpdated, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = next
	s.transactions.outbox.Notify()
	return nil
}

func (s *Blueprints) BlueprintApprovalMatches(ctx context.Context, projectID, path, runID string, revision int64, digest string) (bool, error) {
	count, err := s.transactions.queries.CountMatchingBlueprintApproval(ctx, db.CountMatchingBlueprintApprovalParams{
		ProjectID: projectID, Path: path, WorkflowRunID: db.NullString(runID),
		WorkflowRevision: revision, ContentDigest: digest,
	})
	return count == 1, err
}

func (s *Blueprints) RelocateBlueprintPath(ctx context.Context, projectID, from, to string) ([]string, error) {
	projectID = strings.TrimSpace(projectID)
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if projectID == "" || from == "" || to == "" || from == to {
		return nil, nil
	}
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := s.transactions.queries.WithTx(tx).RelocateWorkflowRunBlueprintPaths(ctx, db.RelocateWorkflowRunBlueprintPathsParams{
		ToPath:    db.NullString(to),
		UpdatedAt: db.FormatTime(time.Now().UTC()),
		ProjectID: projectID,
		FromPath:  db.NullString(from),
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(rows))
	out := make([]string, 0, len(rows))
	for _, sessionID := range rows {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		out = append(out, sessionID)
	}
	return out, nil
}
