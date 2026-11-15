// Package workerrecords stores workers, their rounds, messages, delivery
// state, result dispositions, workspace preparations and prerequisites.
//
// Every mutation runs under an operation identity in one writer transaction:
// replay returns the original receipt, other content under the same identity
// is refused, and the caller's hook (an outbox event) commits with the state.
package workerrecords

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// Role names a party to a worker. The set is open: consumers handle the roles
// they know and refuse the rest, and a new role needs no schema change.
type Role string

const (
	RoleCoordinator Role = "coordinator"
	RoleWorker      Role = "worker"
	RolePerson      Role = "person"
)

func (r Role) valid() bool {
	if r == "" || len(r) > 32 {
		return false
	}
	for _, c := range r {
		if (c < 'a' || c > 'z') && c != '_' {
			return false
		}
	}
	return true
}

// Actor is the authenticated caller, taken from the execution context and
// never from message content. SessionID is the session the call runs in: the
// coordinator's session, or a worker's child session.
type Actor struct {
	SessionID   string
	Role        Role
	PrincipalID string
}

// Op identifies one mutation. Hook runs inside the transaction after the
// state change, so its writes commit or roll back with it.
type Op struct {
	ID   string
	Hook func(ctx context.Context, tx *sql.Tx, receipt any) error
}

var (
	// ErrWorkerNotFound covers a worker outside the caller's session, so a
	// refusal never reveals another session's workers.
	ErrWorkerNotFound      = errors.New("worker not found")
	ErrOperationConflict   = errors.New("operation identity reused with different content")
	ErrOperationRequired   = errors.New("operation identity required")
	ErrInvalidActor        = errors.New("caller identity incomplete")
	ErrCoordinatorOnly     = errors.New("only the worker's coordinator may do this")
	ErrNameTaken           = errors.New("worker name already used in this session")
	ErrInvalidName         = errors.New("worker name must be 1 to 64 characters without surrounding space")
	ErrInvalidAgent        = errors.New("worker agent required")
	ErrInvalidScope        = errors.New("worker scope mode must be read or write")
	ErrBindingTaken        = errors.New("workflow work already bound to another worker")
	ErrBindingInvalid      = errors.New("binding belongs to another session")
	ErrWorkerClosed        = errors.New("worker is closed")
	ErrRoundUnsettled      = errors.New("worker already has an unsettled round")
	ErrRoundNotFound       = errors.New("round not found")
	ErrInvalidRound        = errors.New("round needs a job, an execution target and a valid instruction range")
	ErrJobBinding          = errors.New("job belongs to another worker")
	ErrInvalidMessage      = errors.New("message needs a body or an artifact and valid roles")
	ErrReplyUnknown        = errors.New("reply names no earlier message of this worker")
	ErrPageStale           = errors.New("delivery page is not the outstanding page")
	ErrNoRunningRound      = errors.New("worker has no running round to instruct")
	ErrInvalidProfile      = errors.New("worker agent and scope mode must both be set")
	ErrResultNotFound      = errors.New("result not found")
	ErrAlreadyAccounted    = errors.New("result already accounted for")
	ErrDispositionFinal    = errors.New("result changes already landed or rejected")
	ErrInvalidDisposition  = errors.New("invalid result disposition")
	ErrInvalidPreparation  = errors.New("preparation binding generation must not go backwards")
	ErrReasonRequired      = errors.New("a reason is required")
	ErrPrerequisiteUnknown = errors.New("prerequisite names no worker in this session")
	ErrPrerequisiteSelf    = errors.New("a worker cannot wait on itself")
	ErrPrerequisiteCycle   = errors.New("prerequisites would form a cycle")
	ErrPrerequisiteDepth   = errors.New("prerequisite chain too long to check")
)

// Store reads and writes worker records.
type Store struct {
	handle db.Handle
	q      *db.Queries
	now    func() time.Time
}

// New returns a store over handle.
func New(handle db.Handle) *Store {
	return &Store{handle: handle, q: db.New(handle), now: func() time.Time { return time.Now().UTC() }}
}

func (s *Store) stamp() string { return db.FormatTime(s.now()) }

func (a Actor) check() error {
	if strings.TrimSpace(a.SessionID) == "" || strings.TrimSpace(a.PrincipalID) == "" || !a.Role.valid() {
		return ErrInvalidActor
	}
	return nil
}

// run executes fn once per operation identity. A replay with the same kind
// and request returns the stored receipt without running fn or the hook.
func run[R any](ctx context.Context, s *Store, actor Actor, op Op, kind string, request any, fn func(q *db.Queries, tx *sql.Tx) (R, error)) (R, error) {
	var zero R
	if err := actor.check(); err != nil {
		return zero, err
	}
	if strings.TrimSpace(op.ID) == "" {
		return zero, ErrOperationRequired
	}
	digest, err := requestDigest(kind, actor, request)
	if err != nil {
		return zero, err
	}
	tx, err := s.handle.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	prior, err := q.GetWorkerOperation(ctx, op.ID)
	switch {
	case err == nil:
		if prior.Kind != kind || prior.RequestDigest != digest || prior.SessionID != actor.SessionID {
			return zero, ErrOperationConflict
		}
		var receipt R
		if err := json.Unmarshal([]byte(prior.ReceiptJson), &receipt); err != nil {
			return zero, fmt.Errorf("decode receipt %s: %w", op.ID, err)
		}
		return receipt, nil
	case !db.IsNoRows(err):
		return zero, err
	}
	receipt, err := fn(q, tx)
	if err != nil {
		return zero, err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return zero, err
	}
	if err := q.InsertWorkerOperation(ctx, db.InsertWorkerOperationParams{
		OperationID: op.ID, SessionID: actor.SessionID, Kind: kind, RequestDigest: digest,
		ReceiptJson: string(raw), CreatedAt: s.stamp(),
	}); err != nil {
		return zero, err
	}
	if op.Hook != nil {
		if err := op.Hook(ctx, tx, receipt); err != nil {
			return zero, err
		}
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return receipt, nil
}

func requestDigest(kind string, actor Actor, request any) (string, error) {
	raw, err := json.Marshal(struct {
		Kind    string `json:"kind"`
		Role    Role   `json:"role"`
		Sender  string `json:"sender"`
		Request any    `json:"request"`
	}{kind, actor.Role, actor.PrincipalID, request})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// side reports how the actor relates to the worker.
type side int

const (
	sideNone side = iota
	sideCoordinator
	sideWorker
)

func actorSide(w db.Workers, actor Actor) side {
	switch {
	case actor.SessionID == w.SessionID:
		return sideCoordinator
	case w.ChildSessionID.Valid && actor.SessionID == w.ChildSessionID.String:
		return sideWorker
	default:
		return sideNone
	}
}

// loadWorker returns the worker when the actor may see it.
func loadWorker(ctx context.Context, q *db.Queries, actor Actor, workerID string) (db.Workers, side, error) {
	w, err := q.GetWorker(ctx, strings.TrimSpace(workerID))
	if db.IsNoRows(err) {
		return db.Workers{}, sideNone, ErrWorkerNotFound
	}
	if err != nil {
		return db.Workers{}, sideNone, err
	}
	sd := actorSide(w, actor)
	if sd == sideNone {
		return db.Workers{}, sideNone, ErrWorkerNotFound
	}
	return w, sd, nil
}

// coordinatorWorkerAnyState returns a worker, open or closed, for its
// coordinator; reads of a closed worker's history stay allowed.
func coordinatorWorkerAnyState(ctx context.Context, q *db.Queries, actor Actor, workerID string) (db.Workers, error) {
	w, sd, err := loadWorker(ctx, q, actor, workerID)
	if err != nil {
		return w, err
	}
	if sd != sideCoordinator {
		return w, ErrCoordinatorOnly
	}
	return w, nil
}

// coordinatorWorker returns an open worker for its coordinator.
func coordinatorWorker(ctx context.Context, q *db.Queries, actor Actor, workerID string) (db.Workers, error) {
	w, sd, err := loadWorker(ctx, q, actor, workerID)
	if err != nil {
		return w, err
	}
	if sd != sideCoordinator {
		return w, ErrCoordinatorOnly
	}
	if w.ClosedAt.Valid {
		return w, ErrWorkerClosed
	}
	return w, nil
}

func nullString(s string) sql.NullString {
	s = strings.TrimSpace(s)
	return sql.NullString{String: s, Valid: s != ""}
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: v > 0} }

// Operation returns the receipt an operation identity already recorded for
// this session, so a caller composing several operations can replay its
// first outcome. ok is false for an identity not used yet.
func (s *Store) Operation(ctx context.Context, actor Actor, opID string) (kind string, receipt json.RawMessage, ok bool, err error) {
	if err := actor.check(); err != nil {
		return "", nil, false, err
	}
	prior, err := s.q.GetWorkerOperation(ctx, strings.TrimSpace(opID))
	if db.IsNoRows(err) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	if prior.SessionID != actor.SessionID {
		return "", nil, false, ErrOperationConflict
	}
	return prior.Kind, json.RawMessage(prior.ReceiptJson), true, nil
}
