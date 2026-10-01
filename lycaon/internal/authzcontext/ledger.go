package authzcontext

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/authzledger"
)

// EventStore persists append-only authz_events chains and resolves context refs.
type EventStore interface {
	AppendEvent(ctx context.Context, e Event) error
	AppendEventTx(ctx context.Context, tx *sql.Tx, event Event) error
	ListEvents(ctx context.Context, sessionID string) ([]Event, error)
	LatestContext(ctx context.Context, sessionID string) (*Context, error)
	// LatestContextTx avoids a pool read while the caller holds the write lock.
	LatestContextTx(ctx context.Context, tx *sql.Tx, sessionID string) (*Context, error)
}

// Ledger appends authz decision rows with gate vs record fail modes.
type Ledger struct {
	Store EventStore
	Audit AuditConfig
}

// AppendGate records a granting decision; store failure blocks the grant (fail-closed).
func (l *Ledger) AppendGate(ctx context.Context, in RecordInput) error {
	if l == nil || l.Store == nil {
		return authzledger.ErrSealFailed
	}
	if err := l.append(ctx, in); err != nil {
		return authzledger.ErrSealFailed
	}
	return nil
}

func (l *Ledger) AppendGateTx(ctx context.Context, tx *sql.Tx, in RecordInput) error {
	if l == nil || l.Store == nil || tx == nil {
		return authzledger.ErrSealFailed
	}
	event, err := l.eventWith(ctx, in, func(ctx context.Context, sessionID string) (*Context, error) {
		return l.Store.LatestContextTx(ctx, tx, sessionID)
	})
	if err != nil {
		return authzledger.ErrSealFailed
	}
	if err := l.Store.AppendEventTx(ctx, tx, event); err != nil {
		return authzledger.ErrSealFailed
	}
	return nil
}

// AppendRecord logs a denial best-effort; store failure is logged and ignored.
func (l *Ledger) AppendRecord(ctx context.Context, in RecordInput) {
	if l == nil || l.Store == nil {
		return
	}
	if err := l.append(ctx, in); err != nil {
		slog.WarnContext(ctx, "authzcontext: event append failed (best-effort)", "session_id", in.SessionID, "action", in.Action, "error", err)
	}
}

func (l *Ledger) append(ctx context.Context, in RecordInput) error {
	e, err := l.event(ctx, in)
	if err != nil {
		return err
	}
	return l.Store.AppendEvent(ctx, e)
}

func (l *Ledger) event(ctx context.Context, in RecordInput) (Event, error) {
	return l.eventWith(ctx, in, l.Store.LatestContext)
}

func (l *Ledger) eventWith(ctx context.Context, in RecordInput, latestContext func(context.Context, string) (*Context, error)) (Event, error) {
	sessionID := strings.TrimSpace(in.SessionID)
	if sessionID == "" {
		return Event{}, ErrNilStore
	}
	var contextSeq int
	var configHash string
	if latest, err := latestContext(ctx, sessionID); err != nil {
		return Event{}, err
	} else if latest != nil {
		contextSeq = latest.ContextSeq
		configHash = latest.ConfigHash
	}
	detail := BuildEventDetail(DetailInput{
		ToolCallID:          firstNonEmpty(in.Detail.ToolCallID, authzledger.InvocationToolCall(ctx, sessionID)),
		Tool:                firstNonEmpty(in.ToolName, in.Detail.Tool),
		Args:                in.Detail.Args,
		Files:               in.Detail.Files,
		ProjectDir:          in.Detail.ProjectDir,
		Tier:                in.Detail.Tier,
		RejectCode:          firstNonEmpty(in.RejectCode, in.Detail.RejectCode),
		GrantScope:          in.Detail.GrantScope,
		CaptureArgv:         l.captureArgv(),
		AuthorizationSource: in.Detail.AuthorizationSource,
		SuppressionCause:    in.Detail.SuppressionCause,
		AskFamily:           in.Detail.AskFamily,
		ContentDecision:     in.Detail.ContentDecision,
		BlueprintDigest:     in.Detail.BlueprintDigest,
		CheckpointID:        in.Detail.CheckpointID,
		PlanID:              in.Detail.PlanID,
		ActionDigest:        in.Detail.ActionDigest,
		SelectedOptionID:    in.Detail.SelectedOptionID,
		GrantIDs:            in.Detail.GrantIDs,
		SubjectKind:         in.Detail.SubjectKind,
		SubjectTitle:        in.Detail.SubjectTitle,
		Gate:                in.Detail.Gate,
		Reasons:             in.Detail.Reasons,
		ExternalAccess:      in.Detail.ExternalAccess,
		ExternalAccessInput: in.Detail.ExternalAccessInput,
		ApprovalRules:       in.Detail.ApprovalRules,
		ResolverPolicy:      in.Detail.ResolverPolicy,
	})
	detailJSON, err := marshalEventDetail(detail)
	if err != nil {
		return Event{}, err
	}
	now := time.Now().UTC()
	return Event{
		ID:               uuid.NewString(),
		SessionID:        sessionID,
		HashVersion:      HashVersion1,
		RecordedAt:       now,
		ContextSeq:       contextSeq,
		Action:           in.Action,
		Outcome:          in.Outcome,
		ResolvedBy:       in.ResolvedBy,
		ResolverPersonID: strings.TrimSpace(in.ResolverPersonID),
		ToolName:         strings.TrimSpace(in.ToolName),
		RejectCode:       strings.TrimSpace(in.RejectCode),
		DetailJSON:       detailJSON,
		ConfigHash:       configHash,
	}, nil
}

func (l *Ledger) captureArgv() bool {
	if l == nil {
		return false
	}
	return l.Audit.CaptureRawArgv
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
