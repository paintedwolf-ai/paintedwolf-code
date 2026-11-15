package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

//go:embed worker_records.sql
var workerRecordsSQL []byte

//go:embed worker_records.go
var workerRecordsSource []byte

var workerRecordsNamespace = uuid.MustParse("6f1d7c5e-3b8a-4c2e-9a41-2d7e5b0c8f13")

var delegationLegRetirement = Retirement{
	Table: "delegation_legs",
	Capture: `SELECT d.id, l.id, d.project_id, d.coordinator_session_id, COALESCE(TRIM(d.blueprint_path), ''), COALESCE(TRIM(d.base_head_sha), '')
		FROM delegation_legs l
		JOIN delegations d ON d.id = l.delegation_id
		JOIN sessions s ON s.id = d.coordinator_session_id AND s.project_id = d.project_id
		ORDER BY d.id, l.id`,
	Rebuild: `SELECT DISTINCT delegation_id, leg_id, project_id, session_id, blueprint_path, base_sha
		FROM workers
		WHERE delegation_id IS NOT NULL AND leg_id IS NOT NULL
		ORDER BY delegation_id, leg_id`,
}

var delegationRetirement = Retirement{
	Table: "delegations",
	Capture: `SELECT d.id, d.project_id, d.coordinator_session_id, COALESCE(TRIM(d.blueprint_path), ''), COALESCE(TRIM(d.base_head_sha), '')
		FROM delegations d
		JOIN sessions s ON s.id = d.coordinator_session_id AND s.project_id = d.project_id
		ORDER BY d.id`,
	Rebuild: `SELECT DISTINCT delegation_id, project_id, session_id, blueprint_path, base_sha
		FROM workers
		WHERE delegation_id IS NOT NULL
		ORDER BY delegation_id`,
}

// WorkerRecords adds workers, rounds, messages, deliveries, result
// dispositions, workspace preparations, prerequisites and operation receipts,
// and retires delegations and delegation_legs into workers.
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

type legacyDelegation struct {
	id, projectID, sessionID, task, status string
	phase, workflowRunID, blueprintPath    string
	baseHeadSHA, createdAt                 string
}

type legacyDelegationLeg struct {
	id, title, status, dependsOnJSON      string
	filesJSON, criteriaJSON, prompt       string
	agentType, workerID, createdAt        string
	startedAt, completedAt                string
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
	if err := migrateWorkerDependencies(ctx, tx); err != nil {
		return err
	}
	return migrateDelegationRecords(ctx, tx)
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
			COALESCE(d.id, ''), COALESCE(dl.id, ''), j.created_at, COALESCE(j.completed_at, ''),
			COALESCE(l.id, ''), COALESCE(l.created_at, ''), COALESCE(res.id, '')
		FROM worker_jobs j
		LEFT JOIN sessions c ON c.id = j.child_session_id
		LEFT JOIN workflow_runs r ON r.id = j.workflow_run_id
		LEFT JOIN delegations d ON d.id = j.delegation_id AND d.coordinator_session_id = j.parent_session_id AND d.project_id = j.project_id
		LEFT JOIN delegation_legs dl ON dl.id = j.leg_id AND dl.delegation_id = d.id
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
		agentType := legacyAgentType(group[len(group)-1].agentType)
		counters[agentType]++
		name := legacyWorkerName(agentType, counters[agentType])
		if err := insertLegacyWorker(ctx, tx, sessionID, name, group, boundWork); err != nil {
			return err
		}
	}
	return nil
}

// Revision 4 accepted blank agent types; workers require one.
func legacyAgentType(agentType string) string {
	if t := strings.TrimSpace(agentType); t != "" {
		return t
	}
	return "worker"
}

func legacyWorkerName(agentType string, n int) string {
	suffix := "-" + strconv.Itoa(n)
	base := []rune(agentType)
	if keep := 64 - len(suffix); len(base) > keep {
		base = base[:keep]
	}
	return strings.TrimSpace(string(base)) + suffix
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
		workerID, first.projectID, sessionID, name, legacyAgentType(latest.agentType), legacyScopeMode(latest.scopeJSON), latest.brief, child,
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
		JOIN worker_jobs c ON c.id = p.worker_job_id AND c.status IN ('pending', 'held')
		JOIN worker_rounds cr ON cr.job_id = p.worker_job_id
		JOIN worker_rounds pr ON pr.job_id = p.prerequisite_id
		JOIN workers cw ON cw.id = cr.worker_id
		JOIN workers pw ON pw.id = pr.worker_id AND pw.session_id = cw.session_id
		WHERE cr.worker_id != pr.worker_id`)
	return err
}

func migrateDelegationRecords(ctx context.Context, tx *sql.Tx) error {
	after := ""
	for {
		batch, err := loadLegacyDelegations(ctx, tx, after)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, d := range batch {
			if err := migrateOneDelegation(ctx, tx, d); err != nil {
				return fmt.Errorf("migrate delegation %s: %w", d.id, err)
			}
		}
		after = batch[len(batch)-1].id
	}
	if err := DropReleased(ctx, tx, "source-manifests-and-command-records", delegationLegRetirement); err != nil {
		return err
	}
	return DropReleased(ctx, tx, "source-manifests-and-command-records", delegationRetirement)
}

func loadLegacyDelegations(ctx context.Context, tx *sql.Tx, after string) (out []legacyDelegation, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT d.id, d.project_id, d.coordinator_session_id, d.task, d.status,
			COALESCE(d.phase, ''), CASE WHEN r.id IS NULL THEN '' ELSE r.id END,
			COALESCE(TRIM(d.blueprint_path), ''), COALESCE(TRIM(d.base_head_sha), ''), d.created_at
		FROM delegations d
		JOIN sessions s ON s.id = d.coordinator_session_id AND s.project_id = d.project_id
		LEFT JOIN workflow_runs r ON r.id = d.workflow_run_id
		WHERE d.id > ?
		ORDER BY d.id LIMIT 64`, after)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var d legacyDelegation
		if err := rows.Scan(&d.id, &d.projectID, &d.sessionID, &d.task, &d.status, &d.phase, &d.workflowRunID, &d.blueprintPath, &d.baseHeadSHA, &d.createdAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func loadLegacyDelegationLegs(ctx context.Context, tx *sql.Tx, delegationID string) (out []legacyDelegationLeg, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, title, status, COALESCE(depends_on_json, ''),
			COALESCE(files_json, ''), COALESCE(completion_criteria_json, ''), COALESCE(prompt, ''),
			COALESCE(agent_type, ''), COALESCE(worker_id, ''), created_at,
			COALESCE(started_at, ''), COALESCE(completed_at, '')
		FROM delegation_legs
		WHERE delegation_id = ?
		ORDER BY id`, delegationID)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var l legacyDelegationLeg
		if err := rows.Scan(&l.id, &l.title, &l.status, &l.dependsOnJSON, &l.filesJSON, &l.criteriaJSON, &l.prompt, &l.agentType, &l.workerID, &l.createdAt, &l.startedAt, &l.completedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func migrateOneDelegation(ctx context.Context, tx *sql.Tx, d legacyDelegation) error {
	legs, err := loadLegacyDelegationLegs(ctx, tx, d.id)
	if err != nil {
		return err
	}
	if len(legs) == 0 {
		res, err := tx.ExecContext(ctx, `UPDATE workers SET blueprint_path = ?, base_sha = ?
			WHERE session_id = ? AND delegation_id = ?`, d.blueprintPath, d.baseHeadSHA, d.sessionID, d.id)
		if err != nil {
			return err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if affected > 0 {
			return nil
		}
		name, err := nextLegacyWorkerName(ctx, tx, d.sessionID, "worker")
		if err != nil {
			return err
		}
		var runID any
		phase := ""
		if d.workflowRunID != "" {
			runID = d.workflowRunID
			phase = strings.TrimSpace(d.phase)
		}
		workerID := uuid.NewSHA1(workerRecordsNamespace, []byte("delegation:"+d.id)).String()
		_, err = tx.ExecContext(ctx, `INSERT INTO workers (
				id, project_id, session_id, name, agent_type, scope_mode, brief, delegation_id,
				workflow_run_id, workflow_phase, blueprint_path, base_sha,
				closed_at, closed_reason, operation_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, 'worker', 'read', ?, ?, ?, ?, ?, ?, ?, 'withdrawn', ?, ?, ?)`,
			workerID, d.projectID, d.sessionID, name, d.task, d.id, runID, phase, d.blueprintPath, d.baseHeadSHA,
			d.createdAt, "migration:delegation:"+d.id, d.createdAt, d.createdAt)
		return err
	}
	for _, leg := range legs {
		if err := migrateDelegationLeg(ctx, tx, d, leg); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workers SET blueprint_path = ?, base_sha = ?
		WHERE session_id = ? AND delegation_id = ?`, d.blueprintPath, d.baseHeadSHA, d.sessionID, d.id); err != nil {
		return err
	}
	for _, leg := range legs {
		if d.status != "active" || (leg.status != "pending" && leg.status != "held") {
			continue
		}
		if err := migrateLegDependencies(ctx, tx, d, leg); err != nil {
			return err
		}
	}
	return nil
}

func migrateDelegationLeg(ctx context.Context, tx *sql.Tx, d legacyDelegation, leg legacyDelegationLeg) error {
	if strings.TrimSpace(leg.workerID) != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE workers SET delegation_id = ?, leg_id = ?
			WHERE session_id = ? AND leg_id IS NULL AND (delegation_id IS NULL OR delegation_id = ?)
			  AND EXISTS (SELECT 1 FROM worker_rounds r WHERE r.worker_id = workers.id AND r.job_id = ?)`,
			d.id, leg.id, d.sessionID, d.id, strings.TrimSpace(leg.workerID)); err != nil {
			return err
		}
	}
	filesJSON := normalizeLegacyJSONArray(leg.filesJSON)
	criteriaJSON := normalizeLegacyJSONArray(leg.criteriaJSON)
	res, err := tx.ExecContext(ctx, `UPDATE workers
		SET blueprint_path = ?, base_sha = ?, files_json = ?, criteria_json = ?
		WHERE session_id = ? AND delegation_id = ? AND leg_id = ?`,
		d.blueprintPath, d.baseHeadSHA, filesJSON, criteriaJSON, d.sessionID, d.id, leg.id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	agentType := legacyAgentType(leg.agentType)
	name, err := nextLegacyWorkerName(ctx, tx, d.sessionID, agentType)
	if err != nil {
		return err
	}
	scopeMode := "read"
	if filesJSON != "[]" {
		scopeMode = "write"
	}
	brief := strings.TrimSpace(leg.prompt)
	if brief == "" {
		brief = leg.title
	}
	updatedAt := leg.completedAt
	if updatedAt == "" {
		updatedAt = leg.startedAt
	}
	if updatedAt == "" {
		updatedAt = leg.createdAt
	}
	var closedAt any
	closedReason := ""
	if d.status != "active" || !slices.Contains([]string{"pending", "dispatched", "running", "retry_pending", "held"}, leg.status) {
		closedAt = updatedAt
		closedReason = "withdrawn"
	}
	var runID any
	phase := ""
	if d.workflowRunID != "" {
		runID = d.workflowRunID
		phase = strings.TrimSpace(d.phase)
	}
	workID := ""
	if runID != nil || d.blueprintPath != "" {
		taken, err := legacyWorkSlotTaken(ctx, tx, d.sessionID, d.workflowRunID, phase, d.blueprintPath, leg.id)
		if err != nil {
			return err
		}
		if closedAt != nil || !taken {
			workID = leg.id
		}
	}
	workerID := uuid.NewSHA1(workerRecordsNamespace, []byte("leg:"+d.id+":"+leg.id)).String()
	_, err = tx.ExecContext(ctx, `INSERT INTO workers (
			id, project_id, session_id, name, agent_type, scope_mode, brief,
			delegation_id, leg_id, workflow_run_id, workflow_phase, workflow_work_id,
			blueprint_path, base_sha, files_json, criteria_json,
			closed_at, closed_reason, operation_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workerID, d.projectID, d.sessionID, name, agentType, scopeMode, brief,
		d.id, leg.id, runID, phase, workID,
		d.blueprintPath, d.baseHeadSHA, filesJSON, criteriaJSON,
		closedAt, closedReason, "migration:leg:"+d.id+":"+leg.id, leg.createdAt, updatedAt)
	return err
}

func migrateLegDependencies(ctx context.Context, tx *sql.Tx, d legacyDelegation, leg legacyDelegationLeg) error {
	raw := strings.TrimSpace(leg.dependsOnJSON)
	if raw == "" {
		return nil
	}
	var deps []string
	if json.Unmarshal([]byte(raw), &deps) != nil || len(deps) == 0 {
		return nil
	}
	for _, dep := range deps {
		dep = strings.TrimSpace(dep)
		if dep == "" || dep == leg.id {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO worker_dependencies (
				worker_id, dependency_key, producer_worker_id, operation_id, created_at
			) SELECT cw.id, pw.id, pw.id, 'migration:leg-dep:' || ? || ':' || ? || ':' || ?, ?
			FROM workers cw
			JOIN workers pw ON pw.session_id = cw.session_id AND pw.delegation_id = cw.delegation_id AND pw.leg_id = ?
			WHERE cw.session_id = ? AND cw.delegation_id = ? AND cw.leg_id = ? AND cw.id != pw.id`,
			d.id, leg.id, dep, leg.createdAt, dep, d.sessionID, d.id, leg.id); err != nil {
			return err
		}
	}
	return nil
}

func nextLegacyWorkerName(ctx context.Context, tx *sql.Tx, sessionID, agentType string) (string, error) {
	for n := 1; ; n++ {
		candidate := legacyWorkerName(agentType, n)
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workers WHERE session_id = ? AND name = ?`, sessionID, candidate).Scan(&exists); err != nil {
			return "", err
		}
		if exists == 0 {
			return candidate, nil
		}
	}
}

func legacyWorkSlotTaken(ctx context.Context, tx *sql.Tx, sessionID, runID, phase, blueprintPath, workID string) (bool, error) {
	if strings.TrimSpace(workID) == "" {
		return false, nil
	}
	if runID != "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workers
			WHERE workflow_run_id = ? AND workflow_phase = ? AND workflow_work_id = ? AND closed_at IS NULL`,
			runID, phase, workID).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	if blueprintPath != "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workers
			WHERE session_id = ? AND blueprint_path = ? AND workflow_work_id = ? AND closed_at IS NULL`,
			sessionID, blueprintPath, workID).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func normalizeLegacyJSONArray(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "[]"
	}
	var items []any
	if json.Unmarshal([]byte(raw), &items) != nil {
		return "[]"
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return "[]"
	}
	return string(encoded)
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
// and a disposition per job or retired delegation leg at most.
func workerRecordsWrites(ctx context.Context, store DBTX, _ uint64) (uint64, error) {
	var jobs, briefBytes int64
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(brief)), 0) FROM worker_jobs`).Scan(&jobs, &briefBytes); err != nil {
		return 0, err
	}
	var delegations, taskBytes int64
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(task)), 0) FROM delegations`).Scan(&delegations, &taskBytes); err != nil {
		return 0, err
	}
	var legs, legBytes int64
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(COALESCE(prompt, '')) + length(COALESCE(files_json, '')) + length(COALESCE(completion_criteria_json, ''))), 0) FROM delegation_legs`).Scan(&legs, &legBytes); err != nil {
		return 0, err
	}
	if jobs < 0 || briefBytes < 0 || delegations < 0 || taskBytes < 0 || legs < 0 || legBytes < 0 {
		return 0, fmt.Errorf("negative worker record write count")
	}
	const perRow = 2048
	return uint64(jobs+delegations+legs)*perRow + uint64(briefBytes+taskBytes+legBytes), nil
}
