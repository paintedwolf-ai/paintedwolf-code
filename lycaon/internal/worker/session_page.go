package worker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

// MaxSessionPageSize bounds one worker history read, including direct queue calls.
const MaxSessionPageSize = 500

var ErrInvalidPageLimit = errors.New("worker page limit must be between 1 and 500")

type SessionPageQuery struct {
	ProjectID, SessionID, Cursor string
	Status                       api.WorkerStatus
	Limit                        int
}

type SessionPage struct {
	Workers    []api.WorkerTask
	NextCursor string
}

type sessionWorkerPosition struct {
	Before    time.Time `json:"before"`
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

var sessionWorkerPages = pagecursor.For[sessionWorkerPosition]("session_workers")

func (q SessionPageQuery) scope() string {
	return pagecursor.Scope(q.ProjectID, q.SessionID, string(q.Status))
}

func (q SessionPageQuery) position() (sessionWorkerPosition, error) {
	if q.Cursor == "" {
		return sessionWorkerPosition{Before: time.Now().UTC()}, nil
	}
	p, err := sessionWorkerPages.Decode(q.Cursor, q.scope())
	if err != nil {
		return p, err
	}
	if p.Before.IsZero() || p.CreatedAt.IsZero() || p.ID == "" || p.CreatedAt.After(p.Before) {
		return p, pagecursor.ErrInvalid
	}
	return p, nil
}

func (q SessionPageQuery) page(tasks []api.WorkerTask, p sessionWorkerPosition) (SessionPage, error) {
	page := SessionPage{Workers: tasks}
	if page.Workers == nil {
		page.Workers = []api.WorkerTask{}
	}
	if len(tasks) <= q.Limit {
		return page, nil
	}
	page.Workers = tasks[:q.Limit]
	last := page.Workers[len(page.Workers)-1]
	p.CreatedAt, p.ID = last.CreatedAt, last.ID
	var err error
	page.NextCursor, err = sessionWorkerPages.Encode(q.scope(), p)
	return page, err
}

func (q *sqlQueuePromotion) ListSessionPage(ctx context.Context, query SessionPageQuery) (SessionPage, error) {
	if query.Limit < 1 || query.Limit > MaxSessionPageSize {
		return SessionPage{}, ErrInvalidPageLimit
	}
	p, err := query.position()
	if err != nil {
		return SessionPage{}, err
	}
	rows, err := q.store.queries.ListSessionWorkerPage(ctx, db.ListSessionWorkerPageParams{
		ProjectID: query.ProjectID, ParentSessionID: db.NullString(query.SessionID),
		Status: string(query.Status), BeforeCreatedAt: db.FormatTime(p.Before),
		AfterCreatedAt: db.FormatTime(p.CreatedAt), AfterID: p.ID, PageLimit: int64(query.Limit + 1),
	})
	if err != nil {
		return SessionPage{}, err
	}
	tasks := make([]api.WorkerTask, 0, len(rows))
	for _, row := range rows {
		task, err := jobstate.FromRow(ctx, q.store.db, row)
		if err != nil {
			return SessionPage{}, err
		}
		tasks = append(tasks, *task)
	}
	q.decorateWorkspacePreparations(tasks)
	return query.page(tasks, p)
}

func (q *memoryQueueQuery) ListSessionPage(ctx context.Context, query SessionPageQuery) (SessionPage, error) {
	if query.Limit < 1 || query.Limit > MaxSessionPageSize {
		return SessionPage{}, ErrInvalidPageLimit
	}
	p, err := query.position()
	if err != nil {
		return SessionPage{}, err
	}
	statuses := []api.WorkerStatus{}
	if query.Status != "" {
		statuses = append(statuses, query.Status)
	}
	tasks, err := q.ListBySession(ctx, query.ProjectID, query.SessionID, statuses...)
	if err != nil {
		return SessionPage{}, err
	}
	selected := tasks[:0]
	for _, task := range tasks {
		if task.CreatedAt.After(p.Before) || task.CreatedAt.Before(p.CreatedAt) || task.CreatedAt.Equal(p.CreatedAt) && task.ID <= p.ID {
			continue
		}
		selected = append(selected, task)
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].CreatedAt.Equal(selected[j].CreatedAt) {
			return selected[i].ID < selected[j].ID
		}
		return selected[i].CreatedAt.Before(selected[j].CreatedAt)
	})
	return query.page(selected, p)
}

// SetChildSessionID binds the spawned child session to a worker job.
func (q *memoryQueueQuery) SetChildSessionID(ctx context.Context, jobID, childSessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	job, ok := q.jobs[jobID]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("job not found: %s", jobID)
	}
	job.task.ChildSessionID = strings.TrimSpace(childSessionID)
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	q.refreshBoard(ctx, snapshot, projectID)
	return nil
}

// PublishWorkerProgress emits one progress edge.
func (q *memoryQueueQuery) PublishWorkerProgress(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, _ bool) error {
	if q == nil || q.memoryQueueCore == nil {
		return nil
	}
	workerJobID = strings.TrimSpace(workerJobID)
	if workerJobID == "" {
		return nil
	}
	q.mu.Lock()
	job := q.jobs[workerJobID]
	if job == nil {
		q.mu.Unlock()
		return nil
	}
	workerprogress.Apply(&job.task, snap)
	snapshot := job.task
	projectID := job.projectID
	q.mu.Unlock()
	q.refreshBoard(ctx, snapshot, projectID)
	return nil
}

// recoverPrerequisiteJobs settles consumers that cannot obtain successful input.
func recoverPrerequisiteJobs(ctx context.Context, queue WorkerQueue, blocked func(context.Context) ([]string, error)) ([]api.WorkerTask, error) {
	var canceled []api.WorkerTask
	for {
		ids, err := blocked(ctx)
		if err != nil {
			return canceled, err
		}
		if len(ids) == 0 {
			return canceled, nil
		}
		for _, id := range ids {
			result := api.WorkerResult{Status: "canceled", Summary: "A prerequisite worker cannot supply successful output.", HostAssembled: true}
			if err := queue.Cancel(ctx, id, &result); err != nil {
				return canceled, err
			}
			if task, ok := queue.Get(id); ok {
				canceled = append(canceled, *task)
			}
		}
	}
}

func (q *sqlQueueCore) recoverPrerequisites(ctx context.Context) ([]api.WorkerTask, error) {
	return recoverPrerequisiteJobs(ctx, q.self, q.store.queries.ListBlockedWorkerPrerequisites)
}

func (q *memoryQueueCore) recoverPrerequisites(ctx context.Context) ([]api.WorkerTask, error) {
	return recoverPrerequisiteJobs(ctx, q.self, q.blockedPrerequisites)
}

func (q *memoryQueueCore) blockedPrerequisites(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	var ids []string
	for id, job := range q.jobs {
		if job.task.Status != api.WorkerStatusPending {
			continue
		}
		for _, dependency := range q.withDependencies(job.task).Dependencies {
			if dependency.State == "blocked" {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids, nil
}

const MaxWorkerPrerequisites = 32

var ErrWorkerPrerequisitesPending = errors.New("worker prerequisites not ready")

func parseAfterWorkers(args map[string]any) ([]string, error) {
	raw, exists := args["after_workers"]
	if !exists {
		return nil, nil
	}
	var ids []string
	switch values := raw.(type) {
	case []any:
		for _, v := range values {
			s, ok := v.(string)
			if !ok {
				return nil, invalidTaskCharter("after_workers", "expected_job_ids")
			}
			ids = append(ids, s)
		}
	case []string:
		ids = append(ids, values...)
	default:
		return nil, invalidTaskCharter("after_workers", "expected_job_ids")
	}
	if len(ids) > MaxWorkerPrerequisites {
		return nil, invalidTaskCharter("after_workers", "too_many_prerequisites")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil || seen[id] {
			return nil, invalidTaskCharter("after_workers", "invalid_or_repeated_job_id")
		}
		seen[id] = true
	}
	return ids, nil
}

func validatePrerequisites(ctx context.Context, task api.WorkerTask, get func(string) (*api.WorkerTask, bool)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(task.AfterWorkers) == 0 {
		return nil
	}
	if len(task.AfterWorkers) > MaxWorkerPrerequisites || task.ChildSessionID != "" || task.EffectiveScope().BaseOverlayID != "" {
		return invalidTaskCharter("after_workers", "requires_fresh_primary_workspace")
	}
	seen := make(map[string]bool, len(task.AfterWorkers))
	for _, id := range task.AfterWorkers {
		upstream, ok := get(id)
		if !ok || upstream == nil || id == task.ID || seen[id] || upstream.ParentSessionID != task.ParentSessionID || upstream.ProjectID != task.ProjectID || upstream.WorkspaceRootID != task.WorkspaceRootID || upstream.PrimaryRootPath() != task.PrimaryRootPath() {
			return invalidTaskCharter("after_workers", "requires_distinct_existing_sibling_jobs_in_same_root")
		}
		seen[id] = true
	}
	return nil
}

// prerequisitesReady runs under the queue lock; SQL claims use the same predicate.
func (q *memoryQueueCore) prerequisitesReady(task api.WorkerTask) bool {
	for _, id := range task.AfterWorkers {
		job := q.jobs[id]
		if job == nil || !api.WorkerOutputReady(&job.task) {
			return false
		}
	}
	return true
}

func (q *memoryQueueCore) withDependencies(task api.WorkerTask) api.WorkerTask {
	task.AfterWorkers = append([]string(nil), task.AfterWorkers...)
	task.Dependencies = nil
	for _, id := range task.AfterWorkers {
		var upstream *api.WorkerTask
		if job := q.jobs[id]; job != nil {
			upstream = &job.task
		}
		task.Dependencies = append(task.Dependencies, api.WorkerDependency{WorkerID: id, State: api.WorkerOutputState(upstream)})
	}
	return task
}
