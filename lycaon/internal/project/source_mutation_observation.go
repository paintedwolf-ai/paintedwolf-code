package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
)

// ObservationScope reads the same durable plans used to recover publication.
// Completed or failed-before-application operations cannot suppress observations.
func (s *SourceMutationService) ObservationScope(ctx context.Context, tx *sql.Tx, projectID string) (sourceledger.MutationObservationScope, error) {
	rows, err := tx.QueryContext(ctx, `SELECT plan_json FROM source_mutations WHERE project_id=? AND status IN (?,?)`, projectID, sourceMutationPrepared, sourceMutationFileApplied)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var pending pendingMutationObservations
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var plan sourceMutationPlan
		if err := json.Unmarshal([]byte(raw), &plan); err != nil {
			return nil, fmt.Errorf("decode pending source mutation: %w", err)
		}
		pending = append(pending, plan.observationPaths()...)
	}
	return pending, rows.Err()
}

type pendingMutationPath struct {
	branch sourcebranch.ID
	rootID string
	path   string
}

type pendingMutationObservations []pendingMutationPath

func (paths pendingMutationObservations) Pending(branch sourcebranch.ID, rootID, path string) bool {
	for _, pending := range paths {
		if pending.branch.String() == branch.String() && pending.rootID == rootID && overlappingSourcePaths(pending.path, path) {
			return true
		}
	}
	return false
}

func (p sourceMutationPlan) observationPaths() []pendingMutationPath {
	if !p.Changed {
		return nil
	}
	var paths []pendingMutationPath
	for _, in := range p.ledgerInputs("") {
		if in.Path != "" {
			paths = append(paths, pendingMutationPath{in.BranchID, in.RootID, in.Path})
		}
		fromRoot := in.FromRootID
		if fromRoot == "" {
			fromRoot = in.RootID
		}
		if in.FromPath != "" {
			paths = append(paths, pendingMutationPath{in.BranchID, fromRoot, in.FromPath})
		}
	}
	return paths
}
