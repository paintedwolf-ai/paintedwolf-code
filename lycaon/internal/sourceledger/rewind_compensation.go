package sourceledger

import (
	"context"
	"database/sql"
	"errors"
)

// Compensation retains the selective target across replaced CRDT identities.
// Reuse requires matching boundary and head operations.
func (s *Store) rewindCompensationTarget(ctx context.Context, projectID, headID, targetID string) (*RestorableVersion, error) {
	var versionID string
	err := s.sqlDB.QueryRowContext(ctx, `SELECT original.id
 FROM source_versions current
 JOIN source_operations rollback ON rollback.id=current.operation_id
 JOIN source_versions original ON original.id=current.parent_version_id
 JOIN source_operations rewind ON rewind.id=original.operation_id
 WHERE current.id=? AND current.project_id=?
 AND rollback.cause='session_rewind_rollback' AND rewind.cause='session_rewind'
 AND rollback.batch_id=rewind.batch_id AND rollback.batch_id<>''
 AND COALESCE(original.derived_from_version_id,'')=?
 AND current.path=original.path AND current.root_id=original.root_id`, headID, projectID, targetID).Scan(&versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	version, err := s.ReadRestorableVersion(ctx, projectID, versionID)
	if errors.Is(err, ErrVersionUnavailable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &version, nil
}
