package hitl

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// authorityColumns are one row's native authority columns; all empty for
// structured authority, whose JSON column is then authoritative.
type authorityColumns struct{ format, digest, body string }

func storedAuthority(format string, projection NativeAuthorityProjection) (authorityColumns, error) {
	if format == "" && !projection.Valid() {
		return authorityColumns{}, nil
	}
	if format != NativeAuthorityFormat || !projection.Valid() || projection.Representation() != format {
		return authorityColumns{}, fmt.Errorf("native authority row requires its exact lossless projection")
	}
	return authorityColumns{format: format, digest: projection.Digest(), body: projection.Canonical()}, nil
}

// approvedAuthority is one approved option: the screened copy for JSON
// columns and, for native rows, its lossless projection.
type approvedAuthority struct {
	display ApprovalOption
	native  NativeAuthorityProjection
}

func (a approvedAuthority) columns() (authorityColumns, error) {
	format := ""
	if a.native.Valid() {
		format = NativeAuthorityFormat
	}
	return storedAuthority(format, a.native)
}

// operation is the prepared-operation row recording this approval.
func (a approvedAuthority) operation(checkpointID, sessionID string) (db.PrepareApprovalOperationParams, error) {
	raw, err := json.Marshal(a.display)
	if err != nil {
		return db.PrepareApprovalOperationParams{}, err
	}
	authority, err := a.columns()
	if err != nil {
		return db.PrepareApprovalOperationParams{}, err
	}
	return db.PrepareApprovalOperationParams{
		CheckpointID: checkpointID, SessionID: sessionID, OptionID: a.display.ID, OptionJson: string(raw),
		CreatedAt:       db.FormatTime(time.Now().UTC()),
		AuthorityFormat: authority.format, AuthorityDigest: authority.digest, AuthorityBody: authority.body,
	}, nil
}

// authoritySource names one row's native authority columns. The generic
// display and list readers never select its body.
type authoritySource struct {
	name    string
	measure func(context.Context, *db.Queries) (db.MeasureCheckpointAuthorityRow, error)
	read    func(context.Context, *db.Queries, db.MeasureCheckpointAuthorityRow) (string, error)
}

func checkpointAuthoritySource(id string) authoritySource {
	return authoritySource{
		name: "checkpoint " + id,
		measure: func(ctx context.Context, q *db.Queries) (db.MeasureCheckpointAuthorityRow, error) {
			return q.MeasureCheckpointAuthority(ctx, id)
		},
		read: func(ctx context.Context, q *db.Queries, m db.MeasureCheckpointAuthorityRow) (string, error) {
			return q.ReadCheckpointAuthority(ctx, db.ReadCheckpointAuthorityParams{ID: id, Format: m.AuthorityFormat, Digest: m.AuthorityDigest, BodyBytes: m.BodyBytes})
		},
	}
}

func operationAuthoritySource(checkpointID string) authoritySource {
	return authoritySource{
		name: "approval operation " + checkpointID,
		measure: func(ctx context.Context, q *db.Queries) (db.MeasureCheckpointAuthorityRow, error) {
			row, err := q.MeasureApprovalOperationAuthority(ctx, checkpointID)
			return db.MeasureCheckpointAuthorityRow(row), err
		},
		read: func(ctx context.Context, q *db.Queries, m db.MeasureCheckpointAuthorityRow) (string, error) {
			return q.ReadApprovalOperationAuthority(ctx, db.ReadApprovalOperationAuthorityParams{CheckpointID: checkpointID, Format: m.AuthorityFormat, Digest: m.AuthorityDigest, BodyBytes: m.BodyBytes})
		},
	}
}

func chatGrantAuthoritySource(chatSessionID, id string) authoritySource {
	return authoritySource{
		name: "chat grant " + id,
		measure: func(ctx context.Context, q *db.Queries) (db.MeasureCheckpointAuthorityRow, error) {
			row, err := q.MeasureChatGrantAuthority(ctx, db.MeasureChatGrantAuthorityParams{ChatSessionID: chatSessionID, ID: id})
			return db.MeasureCheckpointAuthorityRow(row), err
		},
		read: func(ctx context.Context, q *db.Queries, m db.MeasureCheckpointAuthorityRow) (string, error) {
			return q.ReadChatGrantAuthority(ctx, db.ReadChatGrantAuthorityParams{ChatSessionID: chatSessionID, ID: id, Format: m.AuthorityFormat, Digest: m.AuthorityDigest, BodyBytes: m.BodyBytes})
		},
	}
}

// nativeAuthorityReader admits measured bodies from one consistent snapshot.
type nativeAuthorityReader struct {
	db      db.Handle
	queries *db.Queries
}

func (s *SQLStore) nativeAuthority() nativeAuthorityReader {
	return nativeAuthorityReader{db: s.db, queries: s.queries}
}

func (s nativeAuthorityReader) read(ctx context.Context, state toolcontract.ReviewStateAdmission, source authoritySource) (restoredAuthority, func(), error) {
	if state == nil {
		return restoredAuthority{}, nil, fmt.Errorf("%s: native authority admission is not configured", source.name)
	}
	var tx *sql.Tx
	var err error
	if reader, ok := s.db.(db.ReadHandle); ok {
		tx, err = reader.BeginReadTx(ctx)
	} else {
		tx, err = s.db.BeginTx(ctx, nil)
	}
	if err != nil {
		return restoredAuthority{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	projection, release, err := (authoritySnapshot{queries: s.queries.WithTx(tx)}).read(ctx, state, source)
	if err != nil {
		return restoredAuthority{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		release()
		return restoredAuthority{}, nil, err
	}
	return projection, release, nil
}

type authoritySnapshot struct{ queries *db.Queries }

func (s authoritySnapshot) read(ctx context.Context, state toolcontract.ReviewStateAdmission, source authoritySource) (restoredAuthority, func(), error) {
	if state == nil {
		return restoredAuthority{}, nil, fmt.Errorf("%s: native authority admission is not configured", source.name)
	}
	q := s.queries
	measured, err := source.measure(ctx, q)
	if err != nil {
		return restoredAuthority{}, nil, fmt.Errorf("%s: measure native authority: %w", source.name, err)
	}
	if measured.AuthorityFormat != NativeAuthorityFormat || measured.BodyBytes <= 0 || measured.BodyBytes > (math.MaxInt64-2048)/2 {
		return restoredAuthority{}, nil, fmt.Errorf("%s holds no known native authority", source.name)
	}
	releaseScan, err := state.ReserveProgramState(ctx, 2048+measured.BodyBytes)
	if err != nil {
		return restoredAuthority{}, nil, err
	}
	if releaseScan == nil {
		return restoredAuthority{}, nil, fmt.Errorf("%s: native authority admission returned no lease", source.name)
	}
	defer releaseScan()
	body, err := source.read(ctx, q, measured)
	if err != nil {
		return restoredAuthority{}, nil, fmt.Errorf("%s: read native authority: %w", source.name, err)
	}
	if int64(len(body)) != measured.BodyBytes {
		return restoredAuthority{}, nil, fmt.Errorf("%s: native authority changed while read", source.name)
	}
	projection, release, err := restoreNativeAuthority(ctx, state, measured.AuthorityFormat, measured.AuthorityDigest, body)
	if err != nil {
		return restoredAuthority{}, nil, fmt.Errorf("%s: %w", source.name, err)
	}
	return projection, release, nil
}

// validateNativeAuthorityDisplay binds restored authority to its screened presentation.
func validateNativeAuthorityDisplay(ctx context.Context, state toolcontract.ReviewStateAdmission, options, display []ApprovalOption) error {
	derived, release, err := nativeAuthorityDisplay(ctx, state, options)
	if err != nil {
		return err
	}
	defer release()
	same, err := sameAuthorityDisplay(derived, display)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("native authority does not match its displayed approval")
	}
	return nil
}

// approvedOption binds the selected authority to its displayed plan and deciding person.
func approvedOption(ctx context.Context, store approvalAuthorityStore, state toolcontract.ReviewStateAdmission, row StoredCheckpoint, plan *ApprovalPlan, display ApprovalOption, resolution Resolution) (ApprovalOption, approvedAuthority, func(), error) {
	if row.AuthorityFormat == "" {
		option := display.grantedBy(resolution)
		return option, approvedAuthority{display: option}, func() {}, nil
	}
	projection, releaseProjection, err := store.nativeAuthority().read(ctx, state, checkpointAuthoritySource(row.ID))
	if err != nil {
		return ApprovalOption{}, approvedAuthority{}, nil, fmt.Errorf("approval authority: %w", err)
	}
	releases := releaseList{releaseProjection}
	fail := func(err error) (ApprovalOption, approvedAuthority, func(), error) {
		releases.release()
		return ApprovalOption{}, approvedAuthority{}, nil, fmt.Errorf("approval authority: %w", err)
	}
	options := projection.options
	if err := validateNativeAuthorityDisplay(ctx, state, options, plan.Options); err != nil {
		return fail(err)
	}
	var option ApprovalOption
	for _, candidate := range options {
		if candidate.ID == display.ID {
			option = candidate
		}
	}
	option, approved, releaseApproved, err := prepareNativeOption(ctx, state, option, display, resolution)
	if err != nil {
		return fail(err)
	}
	releases = append(releases, releaseApproved)
	return option, approved, releases.release, nil
}

// releaseList releases retained native leases in reverse order.
type releaseList []func()

func (r releaseList) release() {
	for n := len(r) - 1; n >= 0; n-- {
		r[n]()
	}
}
