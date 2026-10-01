// Package personactions records the API operations people invoke.
//
// The operation catalog declares which operations are recorded; the API's
// authorization seam writes one row per completed call. Handlers that know
// what an operation touched beyond its path add it with Note.
package personactions

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

// Action is one completed operation a person invoked.
type Action struct {
	ID          string              `json:"id"`
	PersonID    string              `json:"person_id"`
	OperationID string              `json:"operation_id"`
	PathParams  map[string]string   `json:"path_params"`
	Subject     map[string][]string `json:"subject"`
	Status      int                 `json:"status"`
	RecordedAt  time.Time           `json:"recorded_at"`
}

// Store appends and reads person actions.
type Store struct {
	queries *db.Queries
	now     func() time.Time
}

// New records person actions in database.
func New(database db.DBTX) *Store {
	return &Store{queries: db.New(database), now: time.Now}
}

// Record appends one completed action.
func (s *Store) Record(ctx context.Context, a Action) error {
	if strings.TrimSpace(a.PersonID) == "" || strings.TrimSpace(a.OperationID) == "" {
		return fmt.Errorf("person action requires a person and an operation")
	}
	params, err := json.Marshal(nonNil(a.PathParams))
	if err != nil {
		return fmt.Errorf("encode person action path: %w", err)
	}
	subject, err := json.Marshal(nonNil(a.Subject))
	if err != nil {
		return fmt.Errorf("encode person action subject: %w", err)
	}
	return s.queries.InsertPersonAction(ctx, db.InsertPersonActionParams{
		ID: uuid.NewString(), PersonID: a.PersonID, OperationID: a.OperationID,
		PathParamsJson: string(params), SubjectJson: string(subject),
		Status: int64(a.Status), RecordedAt: db.FormatTime(s.now().UTC()),
	})
}

// Recent returns up to limit actions, newest first.
func (s *Store) Recent(ctx context.Context, limit int) ([]Action, error) {
	rows, err := s.queries.ListRecentPersonActions(ctx, int64(limit))
	if err != nil {
		return nil, err
	}
	out := make([]Action, 0, len(rows))
	for _, row := range rows {
		a := Action{ID: row.ID, PersonID: row.PersonID, OperationID: row.OperationID, Status: int(row.Status)}
		if err := json.Unmarshal([]byte(row.PathParamsJson), &a.PathParams); err != nil {
			return nil, fmt.Errorf("decode person action %s path: %w", row.ID, err)
		}
		if err := json.Unmarshal([]byte(row.SubjectJson), &a.Subject); err != nil {
			return nil, fmt.Errorf("decode person action %s subject: %w", row.ID, err)
		}
		if a.RecordedAt, err = db.ParseTime(row.RecordedAt); err != nil {
			return nil, fmt.Errorf("decode person action %s time: %w", row.ID, err)
		}
		out = append(out, a)
	}
	return out, nil
}

func nonNil[M ~map[K]V, K comparable, V any](m M) M {
	if m == nil {
		return M{}
	}
	return m
}

// Subject collects what a recorded operation touched beyond its path.
type Subject struct {
	mu     sync.Mutex
	values map[string][]string
}

type subjectKey struct{}

// WithSubject binds an empty subject for one recorded operation.
func WithSubject(ctx context.Context) (context.Context, *Subject) {
	subject := &Subject{}
	return context.WithValue(ctx, subjectKey{}, subject), subject
}

// Note adds values under key to the operation's subject. It does nothing
// outside a recorded operation.
func Note(ctx context.Context, key string, values ...string) {
	subject, _ := ctx.Value(subjectKey{}).(*Subject)
	key = strings.TrimSpace(key)
	if subject == nil || key == "" || len(values) == 0 {
		return
	}
	subject.mu.Lock()
	defer subject.mu.Unlock()
	if subject.values == nil {
		subject.values = map[string][]string{}
	}
	subject.values[key] = append(subject.values[key], values...)
}

// Values returns the collected subject with each key's values sorted and
// deduplicated.
func (s *Subject) Values() map[string][]string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]string, len(s.values))
	for key, values := range s.values {
		sorted := slices.Clone(values)
		slices.Sort(sorted)
		out[key] = slices.Compact(sorted)
	}
	return out
}
