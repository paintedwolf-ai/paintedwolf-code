package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

//go:embed worker_records.sql
var workerRecordsSQL []byte

//go:embed worker_records.go
var workerRecordsSource []byte

var workerRecordsNamespace = uuid.MustParse("6f1d7c5e-3b8a-4c2e-9a41-2d7e5b0c8f13")

// WorkerRecords adds workers, rounds, messages, deliveries, result
// dispositions, workspace preparations, prerequisites and operation receipts.
// Each child session with jobs becomes one worker, and a job without a child
// session becomes its own. Landed changes with a recorded receipt become
// landed dispositions; nothing else is accounted for.
func WorkerRecords() Part {
	return Part{
		Source:   append(append([]byte(nil), workerRecordsSQL...), workerRecordsSource...),
		Apply:    migrateWorkerRecords,
		Validate: validateWorkerRecords,
		Writes:   workerRecordsWrites,
	}
}

type legacyWorkerJob struct {
	id, projectID, childSessionID, agentType, scopeJSON, brief, status    string
	executionTarget, workflowRunID, workflowPhase, workflowWorkID         string
	delegationID, legID, createdAt, completedAt, landedChangeID, landedAt string
	resultID                                                              string
}

func migrateWorkerRecords(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, string(workerRecordsSQL)); err != nil {
		return err
	}
	after := ""
	for {
		sessions, err := legacyWorkerSessions(ctx, tx, after)
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			break
		}
		for _, sessionID := range sessions {
			if err := migrateSessionWorkers(ctx, tx, sessionID); err != nil {
				return fmt.Errorf("migrate workers of session %s: %w", sessionID, err)
			}
		}
		after = sessions[len(sessions)-1]
	}
	return migrateWorkerDependencies(ctx, tx)
}

func legacyWorkerSessions(ctx context.Context, tx *sql.Tx, after string) (out []string, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT j.parent_session_id FROM worker_jobs j
		JOIN sessions s ON s.id = j.parent_session_id
		WHERE j.parent_session_id > ? ORDER BY j.parent_session_id LIMIT 64`, after)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func loadLegacyWorkerJobs(ctx context.Context, tx *sql.Tx, sessionID string) (out []legacyWorkerJob, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT j.id, j.project_id,
			CASE WHEN c.parent_session_id = j.parent_session_id THEN c.id ELSE '' END,
			j.agent_type, COALESCE(j.scope_json, ''), j.brief, j.status, j.execution_target,
			CASE WHEN r.id IS NULL THEN '' ELSE r.id END, j.workflow_phase, j.workflow_work_id,
			COALESCE(j.delegation_id, ''), COALESCE(j.leg_id, ''), j.created_at, COALESCE(j.completed_at, ''),
			COALESCE(l.id, ''), COALESCE(l.created_at, ''), COALESCE(res.id, '')
		FROM worker_jobs j
		LEFT JOIN sessions c ON c.id = j.child_session_id
		LEFT JOIN workflow_runs r ON r.id = j.workflow_run_id
		LEFT JOIN landed_changes l ON l.worker_job_id = j.id AND j.merge_status = 'merged'
		LEFT JOIN worker_results res ON res.worker_job_id = j.id
		WHERE j.parent_session_id = ?
		ORDER BY j.created_at, j.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var j legacyWorkerJob
		if err := rows.Scan(&j.id, &j.projectID, &j.childSessionID, &j.agentType, &j.scopeJSON, &j.brief, &j.status,
			&j.executionTarget, &j.workflowRunID, &j.workflowPhase, &j.workflowWorkID, &j.delegationID, &j.legID,
			&j.createdAt, &j.completedAt, &j.landedChangeID, &j.landedAt, &j.resultID); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// migrateSessionWorkers groups a session's jobs by child session, in the
// order each group's first job was created, and names workers from their
// agent and a per-agent counter.
func migrateSessionWorkers(ctx context.Context, tx *sql.Tx, sessionID string) error {
	jobs, err := loadLegacyWorkerJobs(ctx, tx, sessionID)
	if err != nil {
		return err
	}
	var order []string
	groups := map[string][]legacyWorkerJob{}
	for _, job := range jobs {
		key := "child:" + job.childSessionID
		if job.childSessionID == "" {
			key = "job:" + job.id
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], job)
	}
	counters := map[string]int{}
	boundWork := map[string]bool{}
	for _, key := range order {
		group := groups[key]
		latest := group[len(group)-1]
		counters[latest.agentType]++
		name := legacyWorkerName(latest.agentType, counters[latest.agentType])
		if err := insertLegacyWorker(ctx, tx, sessionID, name, group, boundWork); err != nil {
			return err
		}
	}
	return nil
}

func legacyWorkerName(agentType string, n int) string {
	base := strings.TrimSpace(agentType)
	if base == "" {
		base = "worker"
	}
	suffix := "-" + strconv.Itoa(n)
	if len(base)+len(suffix) > 64 {
		base = base[:64-len(suffix)]
	}
	return base + suffix
}

func insertLegacyWorker(ctx context.Context, tx *sql.Tx, sessionID, name string, group []legacyWorkerJob, boundWork map[string]bool) error {
	first, latest := group[0], group[len(group)-1]
	// Derived from the first job so every migration route writes the same rows.
	workerID := uuid.NewSHA1(workerRecordsNamespace, []byte(first.id)).String()
	var child any
	if first.childSessionID != "" {
		child = first.childSessionID
	}
	runID, phase, workID := workflowBinding(group, boundWork)
	var delegation, leg any
	if first.delegationID != "" {
		delegation = first.delegationID
		if first.legID != "" {
			leg = first.legID
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workers (
			id, project_id, session_id, name, agent_type, scope_mode, brief, child_session_id,
			delegation_id, leg_id, workflow_run_id, workflow_phase, workflow_work_id,
			next_round_ordinal, operation_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workerID, first.projectID, sessionID, name, latest.agentType, legacyScopeMode(latest.scopeJSON), latest.brief, child,
		delegation, leg, runID, phase, workID,
		len(group)+1, "migration:worker:"+first.id, first.createdAt, latest.createdAt); err != nil {
		return err
	}
	for i, job := range group {
		settled := legacyRoundSettledAt(group, i)
		if _, err := tx.ExecContext(ctx, `INSERT INTO worker_rounds (
				worker_id, ordinal, job_id, execution_target, operation_id, created_at, settled_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			workerID, i+1, job.id, job.executionTarget, "migration:round:"+job.id, job.createdAt, settled); err != nil {
			return err
		}
		if job.landedChangeID != "" && job.resultID != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO worker_result_dispositions (
					result_id, worker_id, change_state, change_operation_id, landed_change_id, changed_at
				) VALUES (?, ?, 'landed', ?, ?, ?)`,
				job.resultID, workerID, "migration:landed:"+job.landedChangeID, job.landedChangeID, job.landedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

// legacyRoundSettledAt settles a terminal job's round when it completed. A
// later job on the same child session was the coordinator starting a new
// round, so an earlier round still open then ends when that job was created.
func legacyRoundSettledAt(group []legacyWorkerJob, i int) any {
	job := group[i]
	switch job.status {
	case "complete", "failed", "canceled":
		if job.completedAt != "" {
			return job.completedAt
		}
		return job.createdAt
	}
	if i+1 < len(group) {
		return group[i+1].createdAt
	}
	return nil
}

// workflowBinding takes the group's first workflow work identity that no
// earlier worker in the session holds; the jobs keep their own bindings.
func workflowBinding(group []legacyWorkerJob, boundWork map[string]bool) (runID any, phase, workID string) {
	for _, job := range group {
		if job.workflowRunID == "" {
			continue
		}
		key := job.workflowRunID + "\x00" + job.workflowPhase + "\x00" + job.workflowWorkID
		if job.workflowWorkID != "" && boundWork[key] {
			continue
		}
		if job.workflowWorkID != "" {
			boundWork[key] = true
		}
		return job.workflowRunID, job.workflowPhase, job.workflowWorkID
	}
	return nil, "", ""
}

func legacyScopeMode(scopeJSON string) string {
	var scope struct {
		Mode string `json:"mode"`
	}
	if strings.TrimSpace(scopeJSON) != "" && json.Unmarshal([]byte(scopeJSON), &scope) == nil && scope.Mode == "write" {
		return "write"
	}
	return "read"
}

// migrateWorkerDependencies carries prerequisites of jobs still held on them
// over to their workers. Settled jobs' prerequisites are history.
func migrateWorkerDependencies(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO worker_dependencies (worker_id, dependency_key, producer_worker_id, operation_id, created_at)
		SELECT cr.worker_id, pr.worker_id, pr.worker_id, 'migration:prerequisite:' || p.worker_job_id || ':' || p.prerequisite_id, c.created_at
		FROM worker_prerequisites p
		JOIN worker_jobs c ON c.id = p.worker_job_id AND c.status = 'held'
		JOIN worker_rounds cr ON cr.job_id = p.worker_job_id
		JOIN worker_rounds pr ON pr.job_id = p.prerequisite_id
		JOIN workers cw ON cw.id = cr.worker_id
		JOIN workers pw ON pw.id = pr.worker_id AND pw.session_id = cw.session_id
		WHERE cr.worker_id != pr.worker_id`)
	return err
}

func validateWorkerRecords(ctx context.Context, tx *sql.Tx) error {
	var unbound int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM worker_jobs j
		JOIN sessions s ON s.id = j.parent_session_id
		WHERE NOT EXISTS (SELECT 1 FROM worker_rounds r WHERE r.job_id = j.id)`).Scan(&unbound); err != nil {
		return err
	}
	if unbound != 0 {
		return fmt.Errorf("worker records: %d jobs of live sessions have no round", unbound)
	}
	return nil
}

// workerRecordsWrites bounds the rows the backfill writes: a worker, a round
// and a disposition per job at most, each with the job's brief.
func workerRecordsWrites(ctx context.Context, store DBTX, _ uint64) (uint64, error) {
	var jobs, briefBytes int64
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(brief)), 0) FROM worker_jobs`).Scan(&jobs, &briefBytes); err != nil {
		return 0, err
	}
	const perJob = 2048
	return uint64(jobs)*perJob + uint64(briefBytes), nil
}
