package hitl

import (
	"context"
	"database/sql"
	"fmt"
	"math"

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

// authoritySource names one row's native authority columns. The generic
// display and list readers never select its body.
type authoritySource struct {
	name    string
	measure func(context.Context, *db.Queries) (db.MeasureCheckpointAuthorityRow, error)
	read    func(context.Context, *db.Queries, db.MeasureCheckpointAuthorityRow) (string, error)
}

func checkpointAuthoritySource(id string) authoritySource {
	return authoritySource{
		name:    "checkpoint " + id,
		measure: func(ctx context.Context, q *db.Queries) (db.MeasureCheckpointAuthorityRow, error) { return q.MeasureCheckpointAuthority(ctx, id) },
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

// nativeAuthorityReader is the one reader of native authority bodies. It
// measures a body and reserves aggregate state before Scan copies it, reads
// the body from that same snapshot only while its identity still matches, and
// restores it through the strict versioned codec. Nothing falls back to JSON.
type nativeAuthorityReader struct {
	db      db.Handle
	queries *db.Queries
}

func (s *SQLStore) nativeAuthority() nativeAuthorityReader {
	return nativeAuthorityReader{db: s.db, queries: s.queries}
}

func (s nativeAuthorityReader) read(ctx context.Context, state toolcontract.ReviewStateAdmission, source authoritySource) (NativeAuthorityProjection, func(), error) {
	if state == nil {
		return NativeAuthorityProjection{}, nil, fmt.Errorf("%s: native authority admission is not configured", source.name)
	}
	var tx *sql.Tx
	var err error
	if reader, ok := s.db.(db.ReadHandle); ok {
		tx, err = reader.BeginReadTx(ctx)
	} else {
		tx, err = s.db.BeginTx(ctx, nil)
	}
	if err != nil {
		return NativeAuthorityProjection{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	measured, err := source.measure(ctx, q)
	if err != nil {
		return NativeAuthorityProjection{}, nil, fmt.Errorf("%s: measure native authority: %w", source.name, err)
	}
	if measured.AuthorityFormat != NativeAuthorityFormat || measured.BodyBytes <= 0 || measured.BodyBytes > (math.MaxInt64-2048)/2 {
		return NativeAuthorityProjection{}, nil, fmt.Errorf("%s holds no known native authority", source.name)
	}
	releaseScan, err := state.ReserveProgramState(ctx, 2048+measured.BodyBytes)
	if err != nil {
		return NativeAuthorityProjection{}, nil, err
	}
	if releaseScan == nil {
		return NativeAuthorityProjection{}, nil, fmt.Errorf("%s: native authority admission returned no lease", source.name)
	}
	defer releaseScan()
	body, err := source.read(ctx, q, measured)
	if err != nil {
		return NativeAuthorityProjection{}, nil, fmt.Errorf("%s: read native authority: %w", source.name, err)
	}
	if int64(len(body)) != measured.BodyBytes {
		return NativeAuthorityProjection{}, nil, fmt.Errorf("%s: native authority changed while read", source.name)
	}
	projection, release, err := RestoreNativeAuthorityProjection(ctx, state, measured.AuthorityFormat, measured.AuthorityDigest, body)
	if err != nil {
		return NativeAuthorityProjection{}, nil, fmt.Errorf("%s: %w", source.name, err)
	}
	if err := tx.Commit(); err != nil {
		release()
		return NativeAuthorityProjection{}, nil, err
	}
	return projection, release, nil
}

func (s *SQLStore) checkpointAuthority(ctx context.Context, state toolcontract.ReviewStateAdmission, checkpointID string) (NativeAuthorityProjection, func(), error) {
	return s.nativeAuthority().read(ctx, state, checkpointAuthoritySource(checkpointID))
}

// restoredNativeOptions restores a row's options and binds them to the
// screened display copy its JSON column holds; any difference refuses.
func restoredNativeOptions(ctx context.Context, state toolcontract.ReviewStateAdmission, projection NativeAuthorityProjection, display []ApprovalOption) ([]ApprovalOption, func(), error) {
	options, releaseOptions, err := projection.Options(ctx, state)
	if err != nil {
		return nil, nil, err
	}
	derived, releaseDerived, err := nativeAuthorityDisplay(ctx, state, options)
	if err != nil {
		releaseOptions()
		return nil, nil, err
	}
	same, err := sameAuthorityDisplay(derived, display)
	releaseDerived()
	if err == nil && !same {
		err = fmt.Errorf("native authority does not match its displayed approval")
	}
	if err != nil {
		releaseOptions()
		return nil, nil, err
	}
	return options, releaseOptions, nil
}

// approvedOption returns the option to install and its durable record. A
// structured row's payload plan is its authority. A native row's option comes
// only from the measured reader, bound to the displayed plan it was shown as.
func approvedOption(ctx context.Context, store approvalAuthorityStore, state toolcontract.ReviewStateAdmission, row StoredCheckpoint, plan *ApprovalPlan, display ApprovalOption, resolution Resolution) (ApprovalOption, approvedAuthority, func(), error) {
	if row.AuthorityFormat == "" {
		option := display.grantedBy(resolution)
		return option, approvedAuthority{display: option}, func() {}, nil
	}
	projection, releaseProjection, err := store.checkpointAuthority(ctx, state, row.ID)
	if err != nil {
		return ApprovalOption{}, approvedAuthority{}, nil, fmt.Errorf("approval authority: %w", err)
	}
	releases := releaseList{releaseProjection}
	fail := func(err error) (ApprovalOption, approvedAuthority, func(), error) {
		releases.release()
		return ApprovalOption{}, approvedAuthority{}, nil, fmt.Errorf("approval authority: %w", err)
	}
	options, releaseOptions, err := restoredNativeOptions(ctx, state, projection, plan.Options)
	if err != nil {
		return fail(err)
	}
	releases = append(releases, releaseOptions)
	var option ApprovalOption
	for _, candidate := range options {
		if candidate.ID == display.ID {
			option = candidate.grantedBy(resolution)
		}
	}
	approved, releaseApproved, err := NewNativeAuthorityProjection(ctx, state, []ApprovalOption{option})
	if err != nil {
		return fail(err)
	}
	releases = append(releases, releaseApproved)
	return option, approvedAuthority{display: display.grantedBy(resolution), native: approved}, releases.release, nil
}

// approvedOption restores the single approved option a native
// operation or chat grant row records, bound to that row's display copy.
func (s nativeAuthorityReader) approvedOption(ctx context.Context, state toolcontract.ReviewStateAdmission, source authoritySource, display ApprovalOption) (ApprovalOption, func(), error) {
	projection, releaseProjection, err := s.read(ctx, state, source)
	if err != nil {
		return ApprovalOption{}, nil, err
	}
	options, releaseOptions, err := restoredNativeOptions(ctx, state, projection, []ApprovalOption{display})
	if err != nil {
		releaseProjection()
		return ApprovalOption{}, nil, fmt.Errorf("%s: %w", source.name, err)
	}
	return options[0], func() { releaseOptions(); releaseProjection() }, nil
}

// releaseList releases retained native leases in reverse order.
type releaseList []func()

func (r releaseList) release() {
	for n := len(r) - 1; n >= 0; n-- {
		r[n]()
	}
}

// chatGrant restores the one delta of the approved option whose chat
// lifetime is this row, bound to the row's screened delta copy.
func (s nativeAuthorityReader) chatGrant(ctx context.Context, state toolcontract.ReviewStateAdmission, chatSessionID, id string, display ApprovalAuthorityDelta) (ApprovalAuthorityDelta, func(), error) {
	source := chatGrantAuthoritySource(chatSessionID, id)
	projection, releaseProjection, err := s.read(ctx, state, source)
	if err != nil {
		return ApprovalAuthorityDelta{}, nil, err
	}
	options, releaseOptions, err := projection.Options(ctx, state)
	if err != nil {
		releaseProjection()
		return ApprovalAuthorityDelta{}, nil, fmt.Errorf("%s: %w", source.name, err)
	}
	release := func() { releaseOptions(); releaseProjection() }
	found, matches := -1, 0
	for n, delta := range options[0].Authority {
		if chat, grantID, ok := delta.ChatLifetime(); ok && chat == chatSessionID && grantID == id {
			found, matches = n, matches+1
		}
	}
	if len(options) != 1 || matches != 1 {
		release()
		return ApprovalAuthorityDelta{}, nil, fmt.Errorf("%s: native authority does not name exactly this grant", source.name)
	}
	derived, releaseDerived, err := nativeAuthorityDisplay(ctx, state, options)
	if err == nil {
		var same bool
		same, err = sameAuthorityDisplay(derived[0].Authority[found], display)
		releaseDerived()
		if err == nil && !same {
			err = fmt.Errorf("native authority does not match its displayed grant")
		}
	}
	if err != nil {
		release()
		return ApprovalAuthorityDelta{}, nil, fmt.Errorf("%s: %w", source.name, err)
	}
	return options[0].Authority[found], release, nil
}
