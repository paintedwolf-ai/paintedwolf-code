// Package homestart records Home submissions: the project, chat and first
// prompt reserved for one client launch id, and how far creating them got.
package homestart

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

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/pkg/api"
)

// Progress is how far creation has committed.
type Progress string

const (
	ProgressReserved       Progress = "reserved"
	ProgressProjectCreated Progress = "project_created"
	ProgressSessionBound   Progress = "session_bound"
)

// Disposition is whether the start still admits work.
type Disposition string

const (
	DispositionActive  Disposition = "active"
	DispositionStopped Disposition = "stopped"
	DispositionRetired Disposition = "retired"
)

// Failure codes recorded on a step that could not complete.
const (
	FailureProjectCreate          = "project_create_failed"
	FailureSessionCreate          = "session_create_failed"
	FailureDraftWorkspaceOccupied = "draft_workspace_occupied"
)

var (
	// ErrNotFound reports no start for this person under the launch id.
	ErrNotFound = errors.New("home start not found")
	// ErrConflict reports a launch id reused with other choices or by another
	// person, or a replacement against a stale revision or operation.
	ErrConflict = errors.New("home start conflict")
	// ErrClosed reports a stopped or retired start.
	ErrClosed = errors.New("home start is closed")
	// ErrAdmitted reports a first prompt that was already admitted.
	ErrAdmitted = errors.New("home start first prompt already admitted")
)

// Record is one person's Home start.
type Record struct {
	LaunchID          string
	PersonID          string
	Choices           *api.HomeStartChoices
	ProjectID         string
	SessionID         string
	Progress          Progress
	Disposition       Disposition
	PromptOperationID string
	Revision          int64
	FailureCode       string
	FailureResumable  bool
}

// Unfinished reports whether creation still has a step to run.
func (r Record) Unfinished() bool {
	return r.Disposition == DispositionActive && r.Progress != ProgressSessionBound &&
		(r.FailureCode == "" || r.FailureResumable)
}

// Store persists Home starts in SQLite.
type Store struct {
	db      db.Handle
	queries *db.Queries
	now     func() time.Time
}

func NewStore(database db.Handle) *Store {
	return &Store{db: database, queries: db.New(database), now: func() time.Time { return time.Now().UTC() }}
}

const storeIDMetaKey = "home_start_store_id"

// StoreID returns this store's durable identity, minting it on first use.
func (s *Store) StoreID(ctx context.Context) (string, error) {
	if err := s.queries.InsertStoreMetaIfAbsent(ctx, db.InsertStoreMetaIfAbsentParams{
		Key: storeIDMetaKey, Value: uuid.NewString(),
	}); err != nil {
		return "", err
	}
	return s.queries.GetStoreMetaValue(ctx, storeIDMetaKey)
}

// Reserve records a launch and its project, chat and first-prompt IDs, or
// returns the launch already recorded under the key.
func (s *Store) Reserve(ctx context.Context, launchID, personID string, choices api.HomeStartChoices) (Record, error) {
	launchID, err := canonicalUUID(launchID)
	if err != nil {
		return Record{}, err
	}
	choices = NormalizeChoices(choices)
	raw, digest, err := encodeChoices(choices)
	if err != nil {
		return Record{}, err
	}
	now := timelayout.Format(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	inserted, err := q.InsertHomeStart(ctx, db.InsertHomeStartParams{
		LaunchID: launchID, PersonID: personID, ChoicesJson: raw, ChoicesDigest: digest,
		ProjectID: uuid.NewString(), SessionID: uuid.NewString(),
		Progress: string(ProgressReserved), Disposition: string(DispositionActive),
		PromptOperationID: launchID, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return Record{}, fmt.Errorf("reserve home start: %w", err)
	}
	if inserted == 1 {
		if err := q.InsertHomeStartOperation(ctx, db.InsertHomeStartOperationParams{OperationID: launchID, LaunchID: launchID}); err != nil {
			return Record{}, fmt.Errorf("reserve home start operation: %w", err)
		}
	}
	row, err := q.GetHomeStart(ctx, launchID)
	if err != nil {
		return Record{}, err
	}
	if row.PersonID != personID {
		return Record{}, ErrConflict
	}
	// A launch stopped before its PUT stays stopped whatever the choices.
	if row.ChoicesDigest != digest && !(row.ChoicesDigest == "" && row.Disposition == string(DispositionStopped)) {
		return Record{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return recordFromRow(row)
}

// Get reads the person's start under the launch id.
func (s *Store) Get(ctx context.Context, launchID, personID string) (Record, error) {
	return s.get(ctx, s.queries, launchID, personID)
}

// Load reads a start for host work that already holds its launch id.
func (s *Store) Load(ctx context.Context, launchID string) (Record, error) {
	row, err := s.queries.GetHomeStart(ctx, launchID)
	if db.IsNoRows(err) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	return recordFromRow(row)
}

func (s *Store) get(ctx context.Context, q *db.Queries, launchID, personID string) (Record, error) {
	launchID, err := canonicalUUID(launchID)
	if err != nil {
		return Record{}, ErrNotFound
	}
	row, err := q.GetHomeStart(ctx, launchID)
	if db.IsNoRows(err) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	if row.PersonID != personID {
		return Record{}, ErrNotFound
	}
	return recordFromRow(row)
}

// Stop fences the start against any further creation or first-prompt
// admission. A Stop for an unknown key records a stopped launch, so a PUT
// that arrives later cannot create work.
func (s *Store) Stop(ctx context.Context, launchID, personID string) (Record, error) {
	launchID, err := canonicalUUID(launchID)
	if err != nil {
		return Record{}, err
	}
	now := timelayout.Format(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.InsertHomeStart(ctx, db.InsertHomeStartParams{
		LaunchID: launchID, PersonID: personID, ChoicesJson: "{}", ChoicesDigest: "",
		Progress: string(ProgressReserved), Disposition: string(DispositionStopped),
		PromptOperationID: launchID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return Record{}, fmt.Errorf("stop home start: %w", err)
	}
	rec, err := s.get(ctx, q, launchID, personID)
	if errors.Is(err, ErrNotFound) {
		return Record{}, ErrConflict
	}
	if err != nil {
		return Record{}, err
	}
	if rec.Disposition == DispositionActive {
		admitted, err := admittedIn(ctx, q, rec)
		if err != nil {
			return Record{}, err
		}
		// After admission the stop belongs to the turn, not the start.
		if !admitted {
			if _, err := q.StopHomeStart(ctx, db.StopHomeStartParams{UpdatedAt: now, LaunchID: launchID}); err != nil {
				return Record{}, err
			}
		}
	}
	if rec, err = s.get(ctx, q, launchID, personID); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// ReplaceFirstPrompt retires an unadmitted first-prompt operation and binds a
// fresh one to the same chat. Repeating a completed replacement returns the
// current record.
func (s *Store) ReplaceFirstPrompt(ctx context.Context, launchID, personID string, expectedRevision int64, current, replacement string) (Record, error) {
	current, err := canonicalUUID(current)
	if err != nil {
		return Record{}, err
	}
	replacement, err = canonicalUUID(replacement)
	if err != nil {
		return Record{}, err
	}
	if current == replacement {
		return Record{}, ErrConflict
	}
	now := timelayout.Format(s.now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	rec, err := s.get(ctx, q, launchID, personID)
	if err != nil {
		return Record{}, err
	}
	old, err := q.GetHomeStartOperation(ctx, current)
	if db.IsNoRows(err) || (err == nil && old.LaunchID != rec.LaunchID) {
		return Record{}, ErrConflict
	}
	if err != nil {
		return Record{}, err
	}
	if old.State == "retired" {
		if old.ReplacedBy == replacement {
			return rec, nil
		}
		return Record{}, ErrConflict
	}
	if rec.Disposition != DispositionActive {
		return Record{}, ErrClosed
	}
	admitted, err := admittedIn(ctx, q, rec)
	if err != nil {
		return Record{}, err
	}
	if admitted {
		return Record{}, ErrAdmitted
	}
	if rec.Revision != expectedRevision {
		return Record{}, ErrConflict
	}
	if _, err := q.RetireHomeStartOperation(ctx, db.RetireHomeStartOperationParams{ReplacedBy: replacement, OperationID: current}); err != nil {
		return Record{}, err
	}
	if err := q.InsertHomeStartOperation(ctx, db.InsertHomeStartOperationParams{OperationID: replacement, LaunchID: rec.LaunchID}); err != nil {
		return Record{}, ErrConflict
	}
	changed, err := q.ReplaceHomeStartPromptOperation(ctx, db.ReplaceHomeStartPromptOperationParams{
		Replacement: replacement, UpdatedAt: now, LaunchID: rec.LaunchID, Current: current, ExpectedRevision: expectedRevision,
	})
	if err != nil {
		return Record{}, err
	}
	if changed != 1 {
		return Record{}, ErrConflict
	}
	if rec, err = s.get(ctx, q, launchID, personID); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// FirstPrompt reports whether the current first-prompt operation was admitted
// into the bound chat, and the receipt's status when it was.
func (s *Store) FirstPrompt(ctx context.Context, rec Record) (admitted bool, status string, err error) {
	if rec.SessionID == "" {
		return false, "", nil
	}
	row, err := s.queries.GetPromptSubmission(ctx, rec.PromptOperationID)
	if db.IsNoRows(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if row.SessionID != rec.SessionID {
		return false, "", nil
	}
	return true, row.Status, nil
}

// FirstTurnIsLatestLive reports whether the admitted first prompt is the
// chat's latest receipt and has not finished.
func (s *Store) FirstTurnIsLatestLive(ctx context.Context, rec Record) (bool, error) {
	if rec.SessionID == "" {
		return false, nil
	}
	row, err := s.queries.LatestPromptSubmissionForSession(ctx, rec.SessionID)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.ID == rec.PromptOperationID && (row.Status == "queued" || row.Status == "running"), nil
}

func admittedIn(ctx context.Context, q *db.Queries, rec Record) (bool, error) {
	if rec.SessionID == "" {
		return false, nil
	}
	row, err := q.GetPromptSubmission(ctx, rec.PromptOperationID)
	if db.IsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.SessionID == rec.SessionID, nil
}

// CommitProgress returns the write that advances a start from one creation
// step to the next inside the transaction creating that step's rows. It fails
// when the start was stopped, retired or advanced meanwhile, so the creating
// transaction rolls back.
func (s *Store) CommitProgress(launchID string, from, to Progress) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		changed, err := s.queries.WithTx(tx).AdvanceHomeStartProgress(ctx, db.AdvanceHomeStartProgressParams{
			Next: string(to), UpdatedAt: timelayout.Format(s.now()), LaunchID: launchID, Current: string(from),
		})
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrClosed
		}
		return nil
	}
}

// Fail records that the step after progress could not complete.
func (s *Store) Fail(ctx context.Context, launchID string, progress Progress, code string, resumable bool) error {
	var flag int64
	if resumable {
		flag = 1
	}
	_, err := s.queries.FailHomeStart(ctx, db.FailHomeStartParams{
		FailureCode: code, FailureResumable: flag, UpdatedAt: timelayout.Format(s.now()),
		LaunchID: launchID, Progress: string(progress),
	})
	return err
}

// ClearResumableFailure lets a repeated PUT retry the failed step.
func (s *Store) ClearResumableFailure(ctx context.Context, launchID string) error {
	_, err := s.queries.ResumeHomeStart(ctx, db.ResumeHomeStartParams{UpdatedAt: timelayout.Format(s.now()), LaunchID: launchID})
	return err
}

// ListUnfinished returns starts whose creation an engine stop interrupted.
func (s *Store) ListUnfinished(ctx context.Context) ([]string, error) {
	return s.queries.ListResumableHomeStarts(ctx)
}

// NormalizeChoices trims choices to the form their digest covers.
func NormalizeChoices(in api.HomeStartChoices) api.HomeStartChoices {
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	in.Model = strings.TrimSpace(in.Model)
	return in
}

func encodeChoices(choices api.HomeStartChoices) (string, string, error) {
	raw, err := json.Marshal(choices)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(raw)
	return string(raw), hex.EncodeToString(sum[:]), nil
}

func canonicalUUID(raw string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%w: launch and operation ids must be UUIDs", ErrConflict)
	}
	return parsed.String(), nil
}

func recordFromRow(row db.HomeStarts) (Record, error) {
	rec := Record{
		LaunchID: row.LaunchID, PersonID: row.PersonID, ProjectID: row.ProjectID, SessionID: row.SessionID,
		Progress: Progress(row.Progress), Disposition: Disposition(row.Disposition),
		PromptOperationID: row.PromptOperationID, Revision: row.Revision,
		FailureCode: row.FailureCode, FailureResumable: row.FailureResumable == 1,
	}
	if row.ChoicesDigest != "" {
		var choices api.HomeStartChoices
		if err := json.Unmarshal([]byte(row.ChoicesJson), &choices); err != nil {
			return Record{}, fmt.Errorf("decode home start choices: %w", err)
		}
		rec.Choices = &choices
	}
	return rec, nil
}
