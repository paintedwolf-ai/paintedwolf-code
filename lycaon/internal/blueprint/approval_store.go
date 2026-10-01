package blueprint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/people"
)

// ApprovalStore projects canonical approval state onto file-backed artifacts.
type ApprovalStore struct {
	db    db.Handle
	authz authzledger.TransactionalRecorder
}

// NewApprovalStore wires the approval table and the authz chain that every
// end-of-grant transition seals into.
func NewApprovalStore(database db.Handle, authz authzledger.TransactionalRecorder) *ApprovalStore {
	return &ApprovalStore{db: database, authz: authz}
}

// grantEnd is one terminal transition of an in-force approval: the row state it
// writes and the event that records it.
type grantEnd struct {
	status     string
	cause      string
	action     string
	resolvedBy string
}

var (
	// Superseding is host-resolved: the reviewed bytes changed, nobody decided.
	grantEndContentChanged = grantEnd{
		status:     "superseded",
		cause:      db.BlueprintGrantCauseContentChanged,
		action:     authzledger.ActionBlueprintSuperseded,
		resolvedBy: authzledger.ResolvedBySystemDeny,
	}
	grantEndBlueprintDeleted = grantEnd{
		status:     "revoked",
		cause:      db.BlueprintGrantCauseBlueprintDeleted,
		action:     authzledger.ActionBlueprintRevoked,
		resolvedBy: authzledger.ResolvedByHuman,
	}
)

func ContentDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (s *ApprovalStore) Approved(ctx context.Context, projectID, path, digest string) (bool, error) {
	var storedDigest, status string
	err := s.db.QueryRowContext(ctx, `SELECT content_digest, status FROM blueprint_approvals WHERE project_id = ? AND path = ?`,
		strings.TrimSpace(projectID), strings.TrimSpace(path)).Scan(&storedDigest, &status)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status == "approved" && storedDigest == strings.TrimSpace(digest), nil
}

// Supersede ends the grant because the reviewed bytes changed.
func (s *ApprovalStore) Supersede(ctx context.Context, projectID, path string) error {
	return s.endGrant(ctx, projectID, path, grantEndContentChanged)
}

// Revoke ends the grant because the blueprint was deleted.
func (s *ApprovalStore) Revoke(ctx context.Context, projectID, path string) error {
	return s.endGrant(ctx, projectID, path, grantEndBlueprintDeleted)
}

// Relocate moves an approval row onto the new convention path.
func (s *ApprovalStore) Relocate(ctx context.Context, projectID, from, to string) error {
	if s == nil || s.db == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if projectID == "" || from == "" || to == "" || from == to {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE blueprint_approvals SET path = ? WHERE project_id = ? AND path = ?`,
		to, projectID, from)
	return err
}

// endGrant transitions an in-force approval and seals it into the granting
// session's chain in the same transaction; only revoked_at / revoked_cause change.
// The sealing identity is the row's own session_id rather than the nullable
// workflow_run_id, so an unlinked run cannot leave the end unledgered.
func (s *ApprovalStore) endGrant(ctx context.Context, projectID, path string, end grantEnd) error {
	if s.authz == nil {
		return authzledger.ErrSealFailed
	}
	resolverPersonID := ""
	if end.resolvedBy == authzledger.ResolvedByHuman {
		person, err := people.Deciding(ctx)
		if err != nil {
			return fmt.Errorf("blueprint grant resolver: %w", err)
		}
		resolverPersonID = person.ID
	}
	projectID = strings.TrimSpace(projectID)
	path = strings.TrimSpace(path)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var digest, sessionID string
	err = tx.QueryRowContext(ctx, `
		SELECT content_digest, session_id
		FROM blueprint_approvals
		WHERE project_id = ? AND path = ? AND status = 'approved'`, projectID, path).Scan(&digest, &sessionID)
	if db.IsNoRows(err) {
		// Nothing in force: no transition, so nothing to seal.
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE blueprint_approvals
		SET status = ?, revoked_at = ?, revoked_cause = ?
		WHERE project_id = ? AND path = ?`,
		end.status, db.FormatTime(time.Now().UTC()), end.cause, projectID, path); err != nil {
		return err
	}
	if err := s.authz.AppendHumanGateTx(ctx, tx, authzledger.HumanGateRecord{
		SessionID:        sessionID,
		Action:           end.action,
		Outcome:          authzledger.OutcomeDenied,
		ResolvedBy:       end.resolvedBy,
		ResolverPersonID: resolverPersonID,
		Files:            []string{path},
		BlueprintDigest:  digest,
	}); err != nil {
		return err
	}
	return tx.Commit()
}
