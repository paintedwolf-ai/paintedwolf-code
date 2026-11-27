// Package fileops journals user file requests independently of their transport.
package fileops

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

var ErrConflict = errors.New("file operation identity conflict")
var ErrNotFound = errors.New("file operation not found")
var ErrNotCancelable = errors.New("file operation has entered its commit phase")

// FirstAnswerKind records which transport response kind was first written for
// an admitted request, persisted on source_file_requests so retries return the
// same kind regardless of timing or process restarts.
type FirstAnswerKind string

const (
	FirstAnswerPending  FirstAnswerKind = ""
	FirstAnswerResult   FirstAnswerKind = "result"
	FirstAnswerAccepted FirstAnswerKind = "accepted"
)

type Request struct {
	ID, ProjectID, PersonID, Operation, Method, URI, InputDigest, RootScope string
	SessionID                                                               string
	Turn                                                                    int
	Body                                                                    []byte
	State, Phase                                                            string
	EntriesProcessed, BytesProcessed                                        int64
	Cancelable                                                              bool
	ResponseStatus                                                          int
	ResponseBody                                                            string
	CreatedAt, UpdatedAt                                                    time.Time
	// CompletedAt is when the request reached its terminal state; nil while
	// it is queued or running.
	CompletedAt     *time.Time
	FirstAnswerKind FirstAnswerKind
}

func (r Request) Terminal() bool {
	return r.State == "completed" || r.State == "failed" || r.State == "canceled" || r.State == "interrupted"
}

type Store struct {
	db     db.Handle
	mu     sync.Mutex
	memory map[string]Request
}

func NewStore(database db.Handle) *Store {
	return &Store{db: database, memory: make(map[string]Request)}
}

const columns = `id,project_id,person_id,operation,method,uri,body,input_digest,root_scope,state,phase,entries_processed,bytes_processed,cancelable,response_status,response_body,created_at,updated_at,session_id,turn,completed_at,first_answer_kind`

func (s *Store) Get(ctx context.Context, id string) (Request, error) {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		r, ok := s.memory[id]
		if !ok {
			return Request{}, ErrNotFound
		}
		return r, nil
	}
	return scanRequest(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM source_file_requests WHERE id=?`, id))
}

func scanRequest(row interface{ Scan(...any) error }) (Request, error) {
	var r Request
	var created, updated string
	var completed sql.NullString
	var firstAnswer string
	err := row.Scan(&r.ID, &r.ProjectID, &r.PersonID, &r.Operation, &r.Method, &r.URI, &r.Body, &r.InputDigest, &r.RootScope, &r.State, &r.Phase, &r.EntriesProcessed, &r.BytesProcessed, &r.Cancelable, &r.ResponseStatus, &r.ResponseBody, &created, &updated, &r.SessionID, &r.Turn, &completed, &firstAnswer)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.FirstAnswerKind = FirstAnswerKind(firstAnswer)
	r.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return r, err
	}
	r.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil || !completed.Valid {
		return r, err
	}
	completedAt, err := time.Parse(time.RFC3339Nano, completed.String)
	r.CompletedAt = &completedAt
	return r, err
}

func completedAtValue(r Request) sql.NullString {
	if r.CompletedAt == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: r.CompletedAt.Format(time.RFC3339Nano), Valid: true}
}

func (s *Store) Insert(ctx context.Context, r Request) error {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, exists := s.memory[r.ID]; exists {
			return ErrConflict
		}
		s.memory[r.ID] = r
		return nil
	}
	if r.Body == nil {
		r.Body = []byte{}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO source_file_requests (`+columns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.ID, r.ProjectID, r.PersonID, r.Operation, r.Method, r.URI, r.Body, r.InputDigest, r.RootScope, r.State, r.Phase, r.EntriesProcessed, r.BytesProcessed, r.Cancelable, r.ResponseStatus, r.ResponseBody, r.CreatedAt.Format(time.RFC3339Nano), r.UpdatedAt.Format(time.RFC3339Nano), r.SessionID, r.Turn, completedAtValue(r), string(r.FirstAnswerKind))
	return err
}

func (s *Store) Update(ctx context.Context, r Request) error {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		existing, exists := s.memory[r.ID]
		if !exists {
			return ErrNotFound
		}
		// first_answer_kind is write-once and never overwritten by state updates.
		r.FirstAnswerKind = existing.FirstAnswerKind
		s.memory[r.ID] = r
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE source_file_requests SET state=?,phase=?,entries_processed=?,bytes_processed=?,cancelable=?,response_status=?,response_body=?,updated_at=?,completed_at=? WHERE id=?`, r.State, r.Phase, r.EntriesProcessed, r.BytesProcessed, r.Cancelable, r.ResponseStatus, r.ResponseBody, r.UpdatedAt.Format(time.RFC3339Nano), completedAtValue(r), r.ID)
	return err
}

// RecordFirstAnswer persists kind on request id if no first answer has been
// recorded yet, and returns the stored kind. Once written, first_answer_kind
// is never rewritten.
func (s *Store) RecordFirstAnswer(ctx context.Context, id string, kind FirstAnswerKind) (FirstAnswerKind, error) {
	if kind != FirstAnswerResult && kind != FirstAnswerAccepted {
		return FirstAnswerPending, errors.New("invalid first answer kind")
	}
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		r, exists := s.memory[id]
		if !exists {
			return FirstAnswerPending, ErrNotFound
		}
		if r.FirstAnswerKind == FirstAnswerPending {
			r.FirstAnswerKind = kind
			s.memory[id] = r
		}
		return r.FirstAnswerKind, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE source_file_requests SET first_answer_kind=? WHERE id=? AND first_answer_kind=''`, string(kind), id); err != nil {
		return FirstAnswerPending, err
	}
	var recorded string
	err := s.db.QueryRowContext(ctx, `SELECT first_answer_kind FROM source_file_requests WHERE id=?`, id).Scan(&recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return FirstAnswerPending, ErrNotFound
	}
	if err != nil {
		return FirstAnswerPending, err
	}
	return FirstAnswerKind(recorded), nil
}

// listedAt orders requests within their group: active work by when it was
// submitted, finished work by when it finished; newest first in both.
func (r Request) listedAt() time.Time {
	if r.CompletedAt != nil {
		return *r.CompletedAt
	}
	return r.CreatedAt
}

// List returns active requests, then finished ones, newest first in each.
func (s *Store) List(ctx context.Context, projectID, personID string) ([]Request, error) {
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		var out []Request
		for _, r := range s.memory {
			if r.ProjectID == projectID && r.PersonID == personID {
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			a, b := out[i], out[j]
			if a.Terminal() != b.Terminal() {
				return !a.Terminal()
			}
			if a.listedAt().Equal(b.listedAt()) {
				return a.ID < b.ID
			}
			return a.listedAt().After(b.listedAt())
		})
		if len(out) > 100 {
			out = out[:100]
		}
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM source_file_requests WHERE project_id=? AND person_id=? ORDER BY CASE WHEN state IN ('queued','running') THEN 0 ELSE 1 END,COALESCE(completed_at, created_at) DESC,id LIMIT 100`, projectID, personID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Recover settles requests after a restart: applied effects complete and
// unfinished work is interrupted for explicit retry.
func (s *Store) Recover(ctx context.Context, reconcile func(context.Context, Request) (Outcome, bool, error)) error {
	cursor := ""
	for {
		requests, err := s.recoveryPage(ctx, cursor)
		if err != nil {
			return err
		}
		if len(requests) == 0 {
			return nil
		}
		for _, r := range requests {
			if err := s.recoverRequest(ctx, r, reconcile); err != nil {
				return err
			}
			cursor = r.ID
		}
	}
}

func (s *Store) recoveryPage(ctx context.Context, after string) ([]Request, error) {
	const limit = 100
	var requests []Request
	if s.db == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, r := range s.memory {
			if r.ID > after && r.State != "completed" && r.State != "canceled" {
				requests = append(requests, r)
			}
		}
		sort.Slice(requests, func(i, j int) bool { return requests[i].ID < requests[j].ID })
		return requests[:min(len(requests), limit)], nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM source_file_requests WHERE state IN ('queued','running','failed','interrupted') AND id>? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, r)
	}
	return requests, rows.Err()
}

func (s *Store) recoverRequest(ctx context.Context, r Request, reconcile func(context.Context, Request) (Outcome, bool, error)) error {
	outcome, applied, err := reconcile(ctx, r)
	if err != nil {
		return err
	}
	if !applied && r.Terminal() {
		return nil
	}
	r.State, r.Phase, r.Cancelable = "interrupted", "interrupted", false
	if applied {
		r.State, r.Phase = "completed", "completed"
		r.ResponseStatus, r.ResponseBody = outcome.Status, outcome.Body
	}
	if r.CompletedAt == nil {
		// The work stopped at its last recorded progress, not at this recovery pass.
		stopped := r.UpdatedAt
		r.CompletedAt = &stopped
	}
	r.UpdatedAt = time.Now().UTC()
	return s.Update(ctx, r)
}
