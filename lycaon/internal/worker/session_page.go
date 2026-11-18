package worker

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/timelayout"
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

func (q *SQLQueue) ListSessionPage(ctx context.Context, query SessionPageQuery) (SessionPage, error) {
	if query.Limit < 1 || query.Limit > MaxSessionPageSize {
		return SessionPage{}, ErrInvalidPageLimit
	}
	p, err := query.position()
	if err != nil {
		return SessionPage{}, err
	}
	rows, err := q.store.queries.ListSessionWorkerPage(ctx, db.ListSessionWorkerPageParams{
		ProjectID: query.ProjectID, ParentSessionID: db.NullString(query.SessionID),
		Status: string(query.Status), BeforeCreatedAt: timelayout.Format(p.Before),
		AfterCreatedAt: timelayout.Format(p.CreatedAt), AfterID: p.ID, PageLimit: int64(query.Limit + 1),
	})
	if err != nil {
		return SessionPage{}, err
	}
	tasks := make([]api.WorkerTask, 0, len(rows))
	for _, row := range rows {
		task, err := q.store.jobs.FromRow(ctx, q.store.db, row)
		if err != nil {
			return SessionPage{}, err
		}
		tasks = append(tasks, *task)
	}
	q.Branches.decorateWorkspacePreparations(tasks)
	return query.page(tasks, p)
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
				return nil, invalidTaskCharter("after_workers", "expected_worker_names")
			}
			ids = append(ids, strings.TrimSpace(s))
		}
	case []string:
		ids = append(ids, values...)
	default:
		return nil, invalidTaskCharter("after_workers", "expected_worker_names")
	}
	if len(ids) > MaxWorkerPrerequisites {
		return nil, invalidTaskCharter("after_workers", "too_many_prerequisites")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			return nil, invalidTaskCharter("after_workers", "empty_or_repeated_worker_name")
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
