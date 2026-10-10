-- name: GetReviewAssignmentRunState :one
SELECT revision, current_phase, status FROM workflow_runs WHERE id = ?;

-- name: InsertWorkflowReviewSubject :exec
INSERT INTO workflow_review_subjects(id, run_id, phase, revision, subject_json)
VALUES (?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING;

-- name: InsertWorkflowReviewAssignment :exec
INSERT INTO workflow_review_assignments(id, run_id, subject_id, phase, work_id, agent, binding_json)
VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING;

-- name: GetWorkflowReviewAssignmentIdentity :one
SELECT binding_json, subject_id FROM workflow_review_assignments WHERE id = ?;

-- name: GetWorkflowReviewBinding :one
SELECT a.binding_json, s.subject_json
FROM workflow_review_assignments a
JOIN workflow_review_subjects s ON s.id = a.subject_id
WHERE a.id = ?;

-- name: ListWorkflowReviewBindings :many
SELECT a.binding_json, s.subject_json
FROM workflow_review_assignments a
JOIN workflow_review_subjects s ON s.id = a.subject_id
WHERE a.run_id = ? AND a.phase = ? AND a.id > ?
ORDER BY a.id LIMIT ?;

-- name: GetWorkflowReviewInputRevision :one
SELECT review_revision FROM workflow_runs WHERE id = ?;

-- name: ActiveWorkflowReviewAssignmentJob :one
SELECT job.id
FROM workflow_review_assignments proposed
JOIN workflow_review_assignments prior
  ON prior.run_id = proposed.run_id AND prior.phase = proposed.phase
  AND prior.work_id = proposed.work_id AND prior.agent = proposed.agent
JOIN worker_jobs job ON job.id = prior.id
WHERE proposed.id = ? AND prior.id != proposed.id
  AND job.status IN ('pending', 'running', 'waiting', 'held')
ORDER BY job.id LIMIT 1;
