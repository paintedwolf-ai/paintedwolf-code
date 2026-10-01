// Package invocation records calls across subsystem-owner boundaries.
package invocation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// Start is the immutable input to a receipt.
type Start struct {
	ProjectID          string
	SessionID          string
	AssistantMessageID string
	ToolCallID         string
	ToolName           string
	Args               map[string]any
	Contract           toolcontract.Contract
}

// Settlement is the terminal receipt projection for the selected operation.
type Settlement struct {
	Status           api.InvocationStatus
	Invoked          bool
	EvidenceKind     string
	EvidenceRef      string
	OwnerRef         string
	SourceRevision   string
	SourceRootDigest string
	// SourceVerdict is a command or verify subsystem owner's terminal reading.
	SourceVerdict string
	Failure       *api.InvocationFailure
	Isolation     *api.InvocationIsolation
}

// InterruptedProjection is an interrupted invocation missing its transcript result.
type InterruptedProjection struct {
	SessionID          string
	AssistantMessageID string
	Receipt            api.InvocationReceipt
}

// Recorder persists invocation receipts.
type Recorder interface {
	Begin(context.Context, Start) (*api.InvocationReceipt, error)
	Settle(context.Context, string, Settlement) (*api.InvocationReceipt, error)
	ListSession(context.Context, string) ([]api.InvocationReceipt, error)
	ListSessionPage(ctx context.Context, sessionID string, cursor string, limit int) (api.InvocationReceiptList, error)
	InterruptRunning(context.Context) (int64, error)
}

// ProjectionRecoveryRecorder finds interrupted receipts whose transcript
// projection is missing without enumerating sessions or transcript history.
type ProjectionRecoveryRecorder interface {
	Recorder
	ListInterruptedWithoutResult(context.Context) ([]InterruptedProjection, error)
}

// SessionInterrupter closes in-flight receipts for one stopped session.
type SessionInterrupter interface {
	InterruptRunningSession(context.Context, string) (int64, error)
}

// SessionProjectionRecoveryRecorder closes and repairs one session without
// scanning its transcript or unrelated sessions.
type SessionProjectionRecoveryRecorder interface {
	SessionInterrupter
	ListInterruptedWithoutResultForSession(context.Context, string) ([]InterruptedProjection, error)
}

// SQLRecorder stores receipts in the main database.
type SQLRecorder struct {
	database db.Handle
	queries  *db.Queries
}

// NewSQLRecorder builds a durable recorder.
func NewSQLRecorder(database db.Handle) *SQLRecorder {
	if database == nil {
		return &SQLRecorder{}
	}
	return &SQLRecorder{database: database, queries: db.New(database)}
}

// Begin records a call before control crosses into its subsystem owner.
func (r *SQLRecorder) Begin(ctx context.Context, in Start) (*api.InvocationReceipt, error) {
	if r == nil || r.queries == nil {
		return nil, fmt.Errorf("invocation recorder is not configured")
	}
	if strings.TrimSpace(in.ProjectID) == "" || strings.TrimSpace(in.SessionID) == "" ||
		strings.TrimSpace(in.ToolCallID) == "" || strings.TrimSpace(in.ToolName) == "" {
		return nil, fmt.Errorf("invocation identity is incomplete")
	}
	if strings.TrimSpace(in.Contract.Owner) == "" {
		return nil, fmt.Errorf("invocation contract subsystem owner is required")
	}
	argsDigest, err := ArgsDigest(in.Args)
	if err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	id := uuid.NewString()
	err = r.queries.CreateInvocationReceipt(ctx, db.CreateInvocationReceiptParams{
		ID: id, ProjectID: in.ProjectID, SessionID: in.SessionID,
		AssistantMessageID: nullableString(in.AssistantMessageID), ToolCallID: in.ToolCallID,
		ToolName: in.ToolName, ContractDigest: in.Contract.Digest(), ArgsDigest: argsDigest,
		Owner: in.Contract.Owner, Lifecycle: in.Contract.Lifecycle.String(),
		Reversibility: in.Contract.Reversibility.String(), EvidencePolicy: string(in.Contract.Evidence()),
		RecoveryPolicy: string(in.Contract.Recovery()), StartedAt: started.Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, fmt.Errorf("begin invocation receipt: %w", err)
	}
	return &api.InvocationReceipt{
		ID: id, Tool: in.ToolName, ToolCallID: in.ToolCallID,
		ContractDigest: in.Contract.Digest(), ArgsDigest: argsDigest,
		Owner: in.Contract.Owner, Lifecycle: in.Contract.Lifecycle.String(),
		Reversibility: in.Contract.Reversibility.String(), EvidencePolicy: string(in.Contract.Evidence()),
		RecoveryPolicy: string(in.Contract.Recovery()), Status: api.InvocationStatusRunning,
		Evidence: api.InvocationEvidence{Kind: "pending"}, StartedAt: started,
	}, nil
}

// Settle records one terminal outcome exactly once. A settlement the ledger
// cannot accept is a host fault: the receipt settles as one and the returned
// SettlementRefusedError carries it.
func (r *SQLRecorder) Settle(ctx context.Context, id string, in Settlement) (*api.InvocationReceipt, error) {
	if r == nil || r.database == nil || r.queries == nil {
		return nil, fmt.Errorf("invocation recorder is not configured")
	}
	if violation := validateSettlement(in); violation != nil {
		return nil, r.refuseSettlement(ctx, id, in.Invoked, violation)
	}
	receipt, err := r.writeSettlement(ctx, id, in)
	if errors.Is(err, errReceiptNotRunning) {
		return nil, &SettlementRefusedError{ReceiptID: id, Violation: err}
	}
	return receipt, err
}

// errReceiptNotRunning marks a second settlement of one receipt.
var errReceiptNotRunning = errors.New("invocation receipt is not running")

func validateSettlement(in Settlement) error {
	if !terminalStatus(in.Status) {
		return fmt.Errorf("terminal invocation status required")
	}
	if strings.TrimSpace(in.EvidenceKind) == "" {
		return fmt.Errorf("invocation evidence kind is required")
	}
	if in.Status == api.InvocationStatusCompleted && in.Failure != nil {
		return fmt.Errorf("completed invocation cannot carry failure")
	}
	if in.Status != api.InvocationStatusCompleted && in.Failure == nil {
		return fmt.Errorf("non-completed invocation failure is required")
	}
	if err := validateIsolationSettlement(in); err != nil {
		return err
	}
	if !sourceVerdict(in.SourceVerdict) {
		return fmt.Errorf("invocation source verdict %q is not a terminal reading", in.SourceVerdict)
	}
	return nil
}

func (r *SQLRecorder) writeSettlement(ctx context.Context, id string, in Settlement) (*api.InvocationReceipt, error) {
	settled := time.Now().UTC()
	invoked := int64(0)
	if in.Invoked {
		invoked = 1
	}
	tx, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("settle invocation receipt: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := r.queries.WithTx(tx)
	rows, err := qtx.SettleInvocationReceipt(ctx, db.SettleInvocationReceiptParams{
		Status: string(in.Status), Invoked: invoked, EvidenceKind: in.EvidenceKind,
		EvidenceRef: in.EvidenceRef, OwnerRef: in.OwnerRef,
		FailureCode: failureCode(in.Failure), FailureClass: failureClass(in.Failure),
		FailureRetryable: failureRetryable(in.Failure), FailureOwnerRef: failureOwnerRef(in.Failure),
		IsolationCode: isolationCode(in.Isolation), IsolationDisposition: isolationDisposition(in.Isolation),
		SourceRevision: in.SourceRevision, SourceRootDigest: in.SourceRootDigest,
		SourceVerdict: in.SourceVerdict,
		SettledAt:     nullableString(settled.Format(time.RFC3339Nano)), ID: id,
	})
	if err != nil {
		return nil, fmt.Errorf("settle invocation receipt: %w", err)
	}
	if rows != 1 {
		return nil, fmt.Errorf("%w: %q", errReceiptNotRunning, id)
	}
	row, err := qtx.GetInvocationReceipt(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load settled invocation receipt: %w", err)
	}
	receipt, err := receiptFromRow(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("settle invocation receipt: commit: %w", err)
	}
	return receipt, nil
}

func validateIsolationSettlement(in Settlement) error {
	var boundaryOutcome isolation.Outcome
	hasBoundary := in.Isolation != nil
	if in.Isolation != nil {
		outcome, ok := isolation.Lookup(strings.TrimSpace(in.Isolation.Code))
		if !ok || string(outcome.Disposition) != strings.TrimSpace(in.Isolation.Disposition) {
			return fmt.Errorf("isolation settlement has unregistered code/disposition: code=%q disposition=%q", in.Isolation.Code, in.Isolation.Disposition)
		}
		boundaryOutcome = outcome
	}
	if in.Failure == nil {
		if hasBoundary && boundaryOutcome.Disposition != isolation.DispositionRetry {
			return fmt.Errorf("terminal isolation outcome %q requires its rejection failure", boundaryOutcome.Code)
		}
		return nil
	}
	outcome, registered := isolation.Lookup(strings.TrimSpace(in.Failure.Code))
	isolationClass := in.Failure.Class == api.FailureClassIsolationRejection
	if registered != isolationClass {
		return fmt.Errorf("isolation settlement code/class mismatch: code=%q class=%q", in.Failure.Code, in.Failure.Class)
	}
	if !registered {
		if hasBoundary && boundaryOutcome.Disposition != isolation.DispositionRetry {
			return fmt.Errorf("terminal isolation outcome %q requires its rejection failure", boundaryOutcome.Code)
		}
		return nil
	}
	if in.Isolation == nil || in.Isolation.Code != outcome.Code {
		return fmt.Errorf("isolation failure %q is not bound to its receipt outcome", in.Failure.Code)
	}
	if in.Failure.Retryable != outcome.Retryable() {
		return fmt.Errorf("isolation settlement retryability mismatch for %q", in.Failure.Code)
	}
	// Invoked stays the owner's fact: a review the owner raises at its effect
	// seam stops an operation that already ran.
	if outcome.Disposition != isolation.DispositionRetry && in.Status != api.InvocationStatusRejected {
		return fmt.Errorf("terminal isolation outcome %q requires a rejected receipt", outcome.Code)
	}
	return nil
}

// ListSession returns receipts in invocation order.
func (r *SQLRecorder) ListSession(ctx context.Context, sessionID string) ([]api.InvocationReceipt, error) {
	if r == nil || r.queries == nil {
		return nil, fmt.Errorf("invocation recorder is not configured")
	}
	rows, err := r.queries.ListSessionInvocationReceipts(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]api.InvocationReceipt, 0, len(rows))
	for _, row := range rows {
		receipt, convertErr := receiptFromRow(row)
		if convertErr != nil {
			return nil, convertErr
		}
		out = append(out, *receipt)
	}
	return out, nil
}

// InvocationPosition is the sealed keyset position for session invocations.
type InvocationPosition struct {
	StartedAt string `json:"started_at"`
	ID        string `json:"id"`
}

// InvocationPages encodes and decodes session invocation cursors.
var InvocationPages = pagecursor.For[InvocationPosition]("session_invocations")

// ListSessionPage returns one page of receipts in invocation order.
func (r *SQLRecorder) ListSessionPage(ctx context.Context, sessionID string, cursor string, limit int) (api.InvocationReceiptList, error) {
	var rows []db.InvocationReceipts
	var err error
	if cursor == "" {
		rows, err = r.queries.ListSessionInvocationReceiptsFirstPage(ctx, db.ListSessionInvocationReceiptsFirstPageParams{
			SessionID:  sessionID,
			LimitCount: int64(limit + 1),
		})
	} else {
		pos, decodeErr := InvocationPages.Decode(cursor, pagecursor.Scope(sessionID))
		if decodeErr != nil {
			return api.InvocationReceiptList{}, decodeErr
		}
		rows, err = r.queries.ListSessionInvocationReceiptsAfterKeyset(ctx, db.ListSessionInvocationReceiptsAfterKeysetParams{
			SessionID:  sessionID,
			StartedAt:  pos.StartedAt,
			ID:         pos.ID,
			LimitCount: int64(limit + 1),
		})
	}
	if err != nil {
		return api.InvocationReceiptList{}, err
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	out := make([]api.InvocationReceipt, 0, len(rows))
	for _, row := range rows {
		receipt, convertErr := receiptFromRow(row)
		if convertErr != nil {
			return api.InvocationReceiptList{}, convertErr
		}
		out = append(out, *receipt)
	}

	var nextCursor string
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		c, err := InvocationPages.Encode(pagecursor.Scope(sessionID), InvocationPosition{
			StartedAt: last.StartedAt,
			ID:        last.ID,
		})
		if err != nil {
			return api.InvocationReceiptList{}, err
		}
		nextCursor = c
	}
	return api.InvocationReceiptList{
		Invocations: out,
		NextCursor:  nextCursor,
	}, nil
}

// ListInterruptedWithoutResult returns one bounded repair page across sessions.
func (r *SQLRecorder) ListInterruptedWithoutResult(ctx context.Context) ([]InterruptedProjection, error) {
	if r == nil || r.queries == nil {
		return nil, fmt.Errorf("invocation recorder is not configured")
	}
	rows, err := r.queries.ListInterruptedInvocationReceiptsWithoutResult(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]InterruptedProjection, 0, len(rows))
	for _, row := range rows {
		receipt, convertErr := receiptFromRow(row)
		if convertErr != nil {
			return nil, convertErr
		}
		out = append(out, InterruptedProjection{SessionID: row.SessionID, AssistantMessageID: row.AssistantMessageID.String, Receipt: *receipt})
	}
	return out, nil
}

// ListInterruptedWithoutResultForSession returns one bounded repair page for a stopped session.
func (r *SQLRecorder) ListInterruptedWithoutResultForSession(ctx context.Context, sessionID string) ([]InterruptedProjection, error) {
	if r == nil || r.queries == nil {
		return nil, fmt.Errorf("invocation recorder is not configured")
	}
	rows, err := r.queries.ListSessionInterruptedInvocationReceiptsWithoutResult(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	out := make([]InterruptedProjection, 0, len(rows))
	for _, row := range rows {
		receipt, convertErr := receiptFromRow(db.InvocationReceipts(row))
		if convertErr != nil {
			return nil, convertErr
		}
		out = append(out, InterruptedProjection{SessionID: row.SessionID, AssistantMessageID: row.AssistantMessageID.String, Receipt: *receipt})
	}
	return out, nil
}

// InterruptRunning closes receipts whose caller did not survive restart.
func (r *SQLRecorder) InterruptRunning(ctx context.Context) (int64, error) {
	if r == nil || r.queries == nil {
		return 0, fmt.Errorf("invocation recorder is not configured")
	}
	return r.queries.InterruptRunningInvocationReceipts(ctx, nullableString(time.Now().UTC().Format(time.RFC3339Nano)))
}

func (r *SQLRecorder) InterruptRunningSession(ctx context.Context, sessionID string) (int64, error) {
	if r == nil || r.queries == nil {
		return 0, fmt.Errorf("invocation recorder is not configured")
	}
	return r.queries.InterruptRunningSessionInvocationReceipts(ctx, db.InterruptRunningSessionInvocationReceiptsParams{
		SettledAt: nullableString(time.Now().UTC().Format(time.RFC3339Nano)),
		SessionID: strings.TrimSpace(sessionID),
	})
}

// ArgsDigest identifies the exact structured argument object.
func ArgsDigest(args map[string]any) (string, error) {
	canonical, err := authzcontext.CanonicalJSON(args)
	if err != nil {
		return "", fmt.Errorf("canonical invocation args: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func terminalStatus(status api.InvocationStatus) bool {
	switch status {
	case api.InvocationStatusCompleted, api.InvocationStatusRejected,
		api.InvocationStatusError, api.InvocationStatusInterrupted:
		return true
	default:
		return false
	}
}

func receiptFromRow(row db.InvocationReceipts) (*api.InvocationReceipt, error) {
	started, err := time.Parse(time.RFC3339Nano, row.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("parse invocation started_at: %w", err)
	}
	var settled *time.Time
	if row.SettledAt.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, row.SettledAt.String)
		if parseErr != nil {
			return nil, fmt.Errorf("parse invocation settled_at: %w", parseErr)
		}
		settled = &value
	}
	evidenceKind := strings.TrimSpace(row.EvidenceKind)
	if evidenceKind == "" {
		evidenceKind = "pending"
	}
	failure := invocationFailureFromRow(row)
	boundary := invocationIsolationFromRow(row)
	status := api.InvocationStatus(row.Status)
	invoked := row.Invoked == 1
	if err := validateIsolationSettlement(Settlement{
		Status: status, Invoked: invoked, Failure: failure, Isolation: boundary,
	}); err != nil {
		return nil, fmt.Errorf("read invocation receipt isolation: %w", err)
	}
	return &api.InvocationReceipt{
		ID: row.ID, Tool: row.ToolName, ToolCallID: row.ToolCallID,
		ContractDigest: row.ContractDigest, ArgsDigest: row.ArgsDigest,
		Owner: row.Owner, Lifecycle: row.Lifecycle, Reversibility: row.Reversibility,
		EvidencePolicy: row.EvidencePolicy, RecoveryPolicy: row.RecoveryPolicy,
		Status: status, Invoked: invoked,
		Evidence:       api.InvocationEvidence{Kind: evidenceKind, Ref: row.EvidenceRef, OwnerRef: row.OwnerRef},
		Failure:        failure,
		Isolation:      boundary,
		SourceRevision: row.SourceRevision, SourceRootDigest: row.SourceRootDigest,
		SourceVerdict: row.SourceVerdict,
		StartedAt:     started, SettledAt: settled,
	}, nil
}

func sourceVerdict(verdict string) bool {
	switch verdict {
	case "", api.SourceVerdictPassed, api.SourceVerdictFailed, api.SourceVerdictUnverifiable:
		return true
	default:
		return false
	}
}

// SourceRevisionForRoot binds a root to its process-local worktree generation.
func SourceRevisionForRoot(root string) (revision, rootDigest string) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", ""
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", ""
	}
	abs = filepath.Clean(abs)
	epoch := repochange.CurrentEpoch(abs)
	if strings.TrimSpace(epoch.BootID) == "" || epoch.Value == 0 {
		return "", ""
	}
	sum := sha256.Sum256([]byte(abs))
	return epoch.BootID + ":" + strconv.FormatUint(epoch.Value, 10), hex.EncodeToString(sum[:])
}

func failureCode(failure *api.InvocationFailure) string {
	if failure == nil {
		return ""
	}
	return strings.TrimSpace(failure.Code)
}

func failureClass(failure *api.InvocationFailure) string {
	if failure == nil {
		return ""
	}
	return strings.TrimSpace(failure.Class)
}

func failureRetryable(failure *api.InvocationFailure) int64 {
	if failure != nil && failure.Retryable {
		return 1
	}
	return 0
}

func failureOwnerRef(failure *api.InvocationFailure) string {
	if failure == nil {
		return ""
	}
	return strings.TrimSpace(failure.OwnerRef)
}

func isolationCode(boundary *api.InvocationIsolation) string {
	if boundary == nil {
		return ""
	}
	return strings.TrimSpace(boundary.Code)
}

func isolationDisposition(boundary *api.InvocationIsolation) string {
	if boundary == nil {
		return ""
	}
	return strings.TrimSpace(boundary.Disposition)
}

func invocationFailureFromRow(row db.InvocationReceipts) *api.InvocationFailure {
	if strings.TrimSpace(row.FailureCode) == "" {
		return nil
	}
	return &api.InvocationFailure{
		Code: row.FailureCode, Class: row.FailureClass,
		Retryable: row.FailureRetryable == 1, OwnerRef: row.FailureOwnerRef,
	}
}

func invocationIsolationFromRow(row db.InvocationReceipts) *api.InvocationIsolation {
	if strings.TrimSpace(row.IsolationCode) == "" {
		return nil
	}
	return &api.InvocationIsolation{
		Code: row.IsolationCode, Disposition: row.IsolationDisposition,
	}
}

func nullableString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}
