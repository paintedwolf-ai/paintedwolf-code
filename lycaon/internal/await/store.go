// Package await persists agent wait leases independently of prompt execution.
package await

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

type Condition struct {
	Kind      string   `json:"kind"`
	Outcome   string   `json:"outcome,omitempty"`
	Code      string   `json:"code,omitempty"`
	Handles   []string `json:"handles,omitempty"`
	URL       string   `json:"url,omitempty"`
	Method    string   `json:"method,omitempty"`
	StatusMin int      `json:"status_min,omitempty"`
	StatusMax int      `json:"status_max,omitempty"`
	Host      string   `json:"host,omitempty"`
	Port      uint16   `json:"port,omitempty"`
	// Report is the host's account of a settled process wake: how the job
	// ended, or what the kernel refused it while it runs.
	Report string `json:"report,omitempty"`
}

type Lease struct {
	ID            string
	SessionID     string
	RootSessionID string
	ProjectID     string
	ProjectDir    string
	ToolCallID    string
	WorkerJobID   string
	ProfileID     string
	Status        string
	Deadline      time.Time
	// UntilComplete ends the wait only on its conditions; Deadline, when set, is its backstop.
	UntilComplete bool
	Conditions    []Condition
	Winner        Condition
	LoopbackPorts []uint16
	Reason        string
}

type Store struct{ DB db.Handle }

func (s *Store) Arm(ctx context.Context, lease Lease) (Lease, error) {
	if s == nil || s.DB == nil {
		return Lease{}, fmt.Errorf("wait store is not configured")
	}
	lease.ID = uuid.NewString()
	lease.Status = "armed"
	lease.SessionID = strings.TrimSpace(lease.SessionID)
	lease.ProjectID = strings.TrimSpace(lease.ProjectID)
	lease.ProfileID = strings.TrimSpace(lease.ProfileID)
	lease.Deadline = lease.Deadline.UTC()
	conditions, err := json.Marshal(lease.Conditions)
	if err != nil {
		return Lease{}, err
	}
	loopbackPorts, err := json.Marshal(lease.LoopbackPorts)
	if err != nil {
		return Lease{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// One coordinator lane admits at most one pending wake.
	if strings.TrimSpace(lease.WorkerJobID) == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE wait_leases SET resume_delivered_at = ?, updated_at = ?
			WHERE session_id = ? AND worker_job_id IS NULL AND status IN ('resolved', 'timed_out') AND resume_delivered_at IS NULL`,
			now, now, lease.SessionID); err != nil {
			return Lease{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wait_leases SET status = 'canceled', updated_at = ?, resolved_at = ? WHERE session_id = ? AND status = 'armed'`, now, now, lease.SessionID); err != nil {
		return Lease{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO wait_leases
        (id, session_id, root_session_id, project_id, project_dir, tool_call_id, worker_job_id, profile_id, status, deadline_at, until_complete, conditions_json, loopback_ports_json, reason, created_at, updated_at)
	        VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, 'armed', ?, ?, ?, ?, ?, ?, ?)`,
		lease.ID, lease.SessionID, lease.RootSessionID, lease.ProjectID, lease.ProjectDir, lease.ToolCallID,
		lease.WorkerJobID, lease.ProfileID, waitDeadlineValue(lease.Deadline), lease.UntilComplete, string(conditions),
		string(loopbackPorts), lease.Reason, now, now); err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

// SettleLease resolves only the named lease.
func (s *Store) SettleLease(ctx context.Context, leaseID, status string, winner Condition) (bool, error) {
	if s == nil || s.DB == nil {
		return false, nil
	}
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return false, fmt.Errorf("invalid wait lease settlement target")
	}
	switch status {
	case "resolved", "timed_out", "interrupted", "canceled":
	default:
		return false, fmt.Errorf("invalid wait terminal status %q", status)
	}
	winnerJSON := ""
	if winner.Kind != "" {
		encoded, err := json.Marshal(winner)
		if err != nil {
			return false, err
		}
		winnerJSON = string(encoded)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.DB.ExecContext(ctx, `UPDATE wait_leases SET status = ?, winner_json = NULLIF(?, ''), updated_at = ?, resolved_at = ? WHERE id = ? AND status = 'armed'`,
		status, winnerJSON, now, now, leaseID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

const leaseColumns = `id, session_id, root_session_id, project_id, project_dir, tool_call_id,
	COALESCE(worker_job_id, ''), profile_id, status, deadline_at, until_complete, conditions_json, loopback_ports_json, reason`

type leaseRow interface{ Scan(dest ...any) error }

func scanLease(row leaseRow) (Lease, error) {
	var lease Lease
	var deadline sql.NullString
	var conditions, loopbackPorts string
	if err := row.Scan(&lease.ID, &lease.SessionID, &lease.RootSessionID, &lease.ProjectID, &lease.ProjectDir,
		&lease.ToolCallID, &lease.WorkerJobID, &lease.ProfileID, &lease.Status, &deadline, &lease.UntilComplete,
		&conditions, &loopbackPorts, &lease.Reason); err != nil {
		return Lease{}, err
	}
	var err error
	if lease.Deadline, err = parseWaitDeadline(deadline); err != nil {
		return Lease{}, err
	}
	if err := json.Unmarshal([]byte(conditions), &lease.Conditions); err != nil {
		return Lease{}, err
	}
	if err := json.Unmarshal([]byte(loopbackPorts), &lease.LoopbackPorts); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (s *Store) Active(ctx context.Context) ([]Lease, error) {
	if s == nil || s.DB == nil {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+leaseColumns+` FROM wait_leases WHERE status = 'armed' ORDER BY deadline_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Lease
	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, lease)
	}
	return out, rows.Err()
}

func (s *Store) ForSession(ctx context.Context, sessionID string) (Lease, bool, error) {
	if s == nil || s.DB == nil {
		return Lease{}, false, nil
	}
	lease, err := scanLease(s.DB.QueryRowContext(ctx, `SELECT `+leaseColumns+` FROM wait_leases WHERE session_id = ? AND status = 'armed'`, strings.TrimSpace(sessionID)))
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, err
	}
	return lease, true, nil
}

// LatestResumeCandidate returns the latest completed or interrupted wait.
func (s *Store) LatestResumeCandidate(ctx context.Context, sessionID string) (Lease, bool, error) {
	if s == nil || s.DB == nil {
		return Lease{}, false, nil
	}
	lease, err := scanLease(s.DB.QueryRowContext(ctx, `SELECT `+leaseColumns+`
		FROM wait_leases WHERE session_id = ? AND status IN ('resolved', 'interrupted', 'timed_out')
		ORDER BY resolved_at DESC, id DESC LIMIT 1`, strings.TrimSpace(sessionID)))
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, err
	}
	return lease, true, nil
}

// PendingWorkerResume returns one undelivered settled worker wait.
func (s *Store) PendingWorkerResume(ctx context.Context, workerJobID string) (string, Condition, bool, error) {
	if s == nil || s.DB == nil {
		return "", Condition{}, false, nil
	}
	var leaseID string
	var winner string
	err := s.DB.QueryRowContext(ctx, `SELECT id, COALESCE(winner_json, '{"kind":"timer","outcome":"timed_out"}')
		FROM wait_leases WHERE worker_job_id = ? AND status IN ('resolved', 'timed_out') AND resume_delivered_at IS NULL
		ORDER BY resolved_at, id LIMIT 1`, strings.TrimSpace(workerJobID)).Scan(&leaseID, &winner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Condition{}, false, nil
	}
	if err != nil {
		return "", Condition{}, false, err
	}
	var condition Condition
	if err := json.Unmarshal([]byte(winner), &condition); err != nil {
		return "", Condition{}, false, err
	}
	return leaseID, condition, true, nil
}

// PendingAgentResumes returns undelivered settled coordinator waits.
func (s *Store) PendingAgentResumes(ctx context.Context) ([]Lease, error) {
	if s == nil || s.DB == nil {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, session_id, COALESCE(winner_json, '{"kind":"timer","outcome":"timed_out"}')
		FROM wait_leases
		WHERE worker_job_id IS NULL AND status IN ('resolved', 'timed_out') AND resume_delivered_at IS NULL
		ORDER BY resolved_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Lease
	for rows.Next() {
		var lease Lease
		var winner string
		if err := rows.Scan(&lease.ID, &lease.SessionID, &winner); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(winner), &lease.Winner); err != nil {
			return nil, err
		}
		out = append(out, lease)
	}
	return out, rows.Err()
}

// MarkResumeDelivered records durable host receipt admission.
func (s *Store) MarkResumeDelivered(ctx context.Context, leaseID string) error {
	if s == nil || s.DB == nil || strings.TrimSpace(leaseID) == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.ExecContext(ctx, `UPDATE wait_leases SET resume_delivered_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('resolved', 'timed_out') AND resume_delivered_at IS NULL`, now, now, strings.TrimSpace(leaseID))
	return err
}

// InterruptSession retires armed waits and undelivered coordinator results.
func (s *Store) InterruptSession(ctx context.Context, sessionID, status string) error {
	if s == nil || s.DB == nil {
		return nil
	}
	if status != "interrupted" && status != "canceled" {
		return fmt.Errorf("invalid wait interruption status %q", status)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.ExecContext(ctx, `UPDATE wait_leases SET status = ?, updated_at = ?, resolved_at = COALESCE(resolved_at, ?)
 WHERE session_id = ? AND (status = 'armed' OR (worker_job_id IS NULL AND status IN ('resolved', 'timed_out') AND resume_delivered_at IS NULL))`,
		status, now, now, strings.TrimSpace(sessionID))
	return err
}

// A missing deadline is an explicit completion-only wait.
func waitDeadlineValue(deadline time.Time) any {
	if deadline.IsZero() {
		return nil
	}
	return deadline.Format(time.RFC3339Nano)
}

func parseWaitDeadline(deadline sql.NullString) (time.Time, error) {
	if !deadline.Valid {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, deadline.String)
}
