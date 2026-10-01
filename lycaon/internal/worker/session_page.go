package worker

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/pagecursor"
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
		Status: string(query.Status), BeforeCreatedAt: db.FormatTime(p.Before),
		AfterCreatedAt: db.FormatTime(p.CreatedAt), AfterID: p.ID, PageLimit: int64(query.Limit + 1),
	})
	if err != nil {
		return SessionPage{}, err
	}
	tasks := make([]api.WorkerTask, 0, len(rows))
	for _, row := range rows {
		task, err := workerTaskFromRow(ctx, q.store.db, row)
		if err != nil {
			return SessionPage{}, err
		}
		tasks = append(tasks, *task)
	}
	q.decorateWorkspacePreparations(tasks)
	return query.page(tasks, p)
}

func (q *InMemoryQueue) ListSessionPage(ctx context.Context, query SessionPageQuery) (SessionPage, error) {
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
