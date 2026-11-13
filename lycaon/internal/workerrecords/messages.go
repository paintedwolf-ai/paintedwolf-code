package workerrecords

import (
	"context"
	"database/sql"
	"strings"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/db"
)

// MaxPageMessages bounds one delivery page.
const MaxPageMessages = 64

// Message is one stored message.
type Message struct {
	ID            string `json:"id"`
	WorkerID      string `json:"worker_id"`
	Seq           int64  `json:"seq"`
	SenderRole    Role   `json:"sender_role"`
	SenderID      string `json:"sender_id"`
	RecipientRole Role   `json:"recipient_role"`
	RoundOrdinal  int64  `json:"round_ordinal,omitempty"`
	Body          string `json:"body,omitempty"`
	ArtifactRef   string `json:"artifact_ref,omitempty"`
	ReplyToSeq    int64  `json:"reply_to_seq,omitempty"`
	NeedsReply    bool   `json:"needs_reply"`
	CreatedAt     string `json:"created_at"`
}

func messageFromRow(m db.WorkerMessages) Message {
	return Message{
		ID: m.ID, WorkerID: m.WorkerID, Seq: m.Seq, SenderRole: Role(m.SenderRole), SenderID: m.SenderID,
		RecipientRole: Role(m.RecipientRole), RoundOrdinal: m.RoundOrdinal.Int64, Body: m.Body,
		ArtifactRef: m.ArtifactRef, ReplyToSeq: m.ReplyToSeq.Int64, NeedsReply: m.NeedsReply == 1, CreatedAt: m.CreatedAt,
	}
}

// MessageSpec is a message's content. Sender, recipient and originating
// round come from the caller's identity and the worker's state.
type MessageSpec struct {
	Body        string `json:"body,omitempty"`
	ArtifactRef string `json:"artifact_ref,omitempty"`
	ReplyToSeq  int64  `json:"reply_to_seq,omitempty"`
	NeedsReply  bool   `json:"needs_reply,omitempty"`
}

// MessageReceipt names a stored message.
type MessageReceipt struct {
	MessageID    string `json:"message_id"`
	WorkerID     string `json:"worker_id"`
	Seq          int64  `json:"seq"`
	RoundOrdinal int64  `json:"round_ordinal,omitempty"`
}

// AppendMessage stores a message on a worker. The coordinator's session
// writes to the worker; the worker's child session writes to its coordinator,
// from its unsettled round.
func (s *Store) AppendMessage(ctx context.Context, actor Actor, op Op, workerID string, spec MessageSpec) (MessageReceipt, error) {
	spec.ArtifactRef = strings.TrimSpace(spec.ArtifactRef)
	if strings.TrimSpace(spec.Body) == "" && spec.ArtifactRef == "" {
		return MessageReceipt{}, ErrInvalidMessage
	}
	return run(ctx, s, actor, op, "append_message", []any{workerID, spec}, func(q *db.Queries, _ *sql.Tx) (MessageReceipt, error) {
		w, sd, err := loadWorker(ctx, q, actor, workerID)
		if err != nil {
			return MessageReceipt{}, err
		}
		if w.ClosedAt.Valid {
			return MessageReceipt{}, ErrWorkerClosed
		}
		// Every message belongs to the round it was sent during. A coordinator
		// instructs a running round here; a stopped worker starts one with
		// StartRound instead.
		recipient := RoleWorker
		var round sql.NullInt64
		r, err := q.GetUnsettledWorkerRound(ctx, w.ID)
		switch {
		case err == nil:
			round = sql.NullInt64{Int64: r.Ordinal, Valid: true}
		case !db.IsNoRows(err):
			return MessageReceipt{}, err
		case sd == sideCoordinator:
			return MessageReceipt{}, ErrNoRunningRound
		}
		if sd == sideWorker {
			recipient = RoleCoordinator
		}
		if spec.ReplyToSeq != 0 {
			if spec.ReplyToSeq < 0 || spec.ReplyToSeq >= w.NextMessageSeq {
				return MessageReceipt{}, ErrReplyUnknown
			}
		}
		now := s.stamp()
		seq, err := q.AllocateWorkerMessageSeq(ctx, db.AllocateWorkerMessageSeqParams{UpdatedAt: now, ID: w.ID})
		if err != nil {
			return MessageReceipt{}, err
		}
		id := uuid.Must(uuid.NewV7()).String()
		needs := int64(0)
		if spec.NeedsReply {
			needs = 1
		}
		if err := q.InsertWorkerMessage(ctx, db.InsertWorkerMessageParams{
			ID: id, WorkerID: w.ID, Seq: seq, SenderRole: string(actor.Role), SenderID: actor.PrincipalID,
			RecipientRole: string(recipient), RoundOrdinal: round, Body: spec.Body, ArtifactRef: spec.ArtifactRef,
			ReplyToSeq: nullInt(spec.ReplyToSeq), NeedsReply: needs, OperationID: op.ID, CreatedAt: now,
		}); err != nil {
			return MessageReceipt{}, err
		}
		return MessageReceipt{MessageID: id, WorkerID: w.ID, Seq: seq, RoundOrdinal: round.Int64}, nil
	})
}

// Page is the outstanding delivery page for one recipient session.
type Page struct {
	ID       string    `json:"id"`
	Messages []Message `json:"messages"`
}

// OpenPage returns the messages a recipient session has not yet been shown,
// oldest first and bounded. Until CommitPage records a successful response the
// same page is returned again, so a failed request replays it.
func (s *Store) OpenPage(ctx context.Context, actor Actor, workerID string, generation int64) (Page, error) {
	if err := actor.check(); err != nil {
		return Page{}, err
	}
	if generation < 0 {
		return Page{}, ErrPageStale
	}
	tx, err := s.handle.BeginTx(ctx, nil)
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	w, sd, err := loadWorker(ctx, q, actor, workerID)
	if err != nil {
		return Page{}, err
	}
	recipient := RoleWorker
	if sd == sideCoordinator {
		recipient = RoleCoordinator
	}
	// A worker reads only its running round's instructions after those the
	// round started with, which reached it in the round's prompt. One that
	// missed its round stays undelivered and is reported as not applied.
	var round sql.NullInt64
	after := int64(0)
	if recipient == RoleWorker {
		r, err := q.GetUnsettledWorkerRound(ctx, w.ID)
		if db.IsNoRows(err) {
			return Page{}, nil
		}
		if err != nil {
			return Page{}, err
		}
		round, after = sql.NullInt64{Int64: r.Ordinal, Valid: true}, r.InstructionLastSeq.Int64
	}
	key := db.GetWorkerMessageDeliveryParams{WorkerID: w.ID, RecipientSessionID: actor.SessionID, Generation: generation}
	state, err := q.GetWorkerMessageDelivery(ctx, key)
	if err != nil && !db.IsNoRows(err) {
		return Page{}, err
	}
	if err == nil && state.PageID != "" {
		msgs, err := pageMessages(ctx, q, w.ID, recipient, round, state.PageFirstSeq.Int64-1, state.PageLastSeq.Int64)
		if err != nil {
			return Page{}, err
		}
		return Page{ID: state.PageID, Messages: msgs}, tx.Commit()
	}
	rows, err := q.ListWorkerMessagesAfter(ctx, db.ListWorkerMessagesAfterParams{
		WorkerID: w.ID, AfterSeq: max(state.DeliveredThroughSeq, after), RecipientRole: string(recipient), RoundOrdinal: round, PageLimit: MaxPageMessages,
	})
	if err != nil {
		return Page{}, err
	}
	if len(rows) == 0 {
		return Page{}, nil
	}
	page := Page{ID: uuid.Must(uuid.NewV7()).String()}
	for _, m := range rows {
		page.Messages = append(page.Messages, messageFromRow(m))
	}
	if _, err := q.OpenWorkerMessagePage(ctx, db.OpenWorkerMessagePageParams{
		WorkerID: w.ID, RecipientSessionID: actor.SessionID, Generation: generation, PageID: page.ID,
		PageFirstSeq: nullInt(rows[0].Seq), PageLastSeq: nullInt(rows[len(rows)-1].Seq), UpdatedAt: s.stamp(),
	}); err != nil {
		return Page{}, err
	}
	return page, tx.Commit()
}

func pageMessages(ctx context.Context, q *db.Queries, workerID string, recipient Role, round sql.NullInt64, after, last int64) ([]Message, error) {
	rows, err := q.ListWorkerMessagesAfter(ctx, db.ListWorkerMessagesAfterParams{
		WorkerID: workerID, AfterSeq: after, RecipientRole: string(recipient), RoundOrdinal: round, PageLimit: MaxPageMessages,
	})
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, m := range rows {
		if m.Seq > last {
			break
		}
		out = append(out, messageFromRow(m))
	}
	return out, nil
}

// CommitPage records that a response including the page succeeded.
// Committing a page already committed is a no-op; any other page is stale.
func (s *Store) CommitPage(ctx context.Context, actor Actor, workerID string, generation int64, pageID string) error {
	if err := actor.check(); err != nil {
		return err
	}
	tx, err := s.handle.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	w, _, err := loadWorker(ctx, q, actor, workerID)
	if err != nil {
		return err
	}
	n, err := q.CommitWorkerMessagePage(ctx, db.CommitWorkerMessagePageParams{
		UpdatedAt: s.stamp(), WorkerID: w.ID, RecipientSessionID: actor.SessionID, Generation: generation, PageID: strings.TrimSpace(pageID),
	})
	if err != nil {
		return err
	}
	if n == 0 {
		state, err := q.GetWorkerMessageDelivery(ctx, db.GetWorkerMessageDeliveryParams{WorkerID: w.ID, RecipientSessionID: actor.SessionID, Generation: generation})
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		if err != nil || state.PageID != "" {
			return ErrPageStale
		}
		return tx.Commit()
	}
	return tx.Commit()
}

// History reads a worker's messages before a sequence, newest first. Reading
// history never changes what counts as delivered.
func (s *Store) History(ctx context.Context, actor Actor, workerID string, beforeSeq int64, limit int) ([]Message, error) {
	if err := actor.check(); err != nil {
		return nil, err
	}
	w, _, err := loadWorker(ctx, s.q, actor, workerID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxPageMessages {
		limit = MaxPageMessages
	}
	if beforeSeq <= 0 {
		beforeSeq = w.NextMessageSeq
	}
	rows, err := s.q.ListWorkerMessagesBefore(ctx, db.ListWorkerMessagesBeforeParams{WorkerID: w.ID, BeforeSeq: beforeSeq, PageLimit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, m := range rows {
		out = append(out, messageFromRow(m))
	}
	return out, nil
}

// NotApplied lists the instructions queued for a round that its worker never
// received before the round ended. They start nothing; the coordinator sees
// them with the round's result and decides whether to send them again.
func (s *Store) NotApplied(ctx context.Context, actor Actor, workerID string, ordinal int64) ([]Message, error) {
	if err := actor.check(); err != nil {
		return nil, err
	}
	w, err := coordinatorWorkerAnyState(ctx, s.q, actor, workerID)
	if err != nil {
		return nil, err
	}
	r, err := s.q.GetWorkerRound(ctx, db.GetWorkerRoundParams{WorkerID: w.ID, Ordinal: ordinal})
	if db.IsNoRows(err) {
		return nil, ErrRoundNotFound
	}
	if err != nil {
		return nil, err
	}
	if !r.SettledAt.Valid {
		return nil, nil
	}
	delivered := int64(0)
	if w.ChildSessionID.Valid {
		if delivered, err = s.q.GetWorkerDeliveredThrough(ctx, db.GetWorkerDeliveredThroughParams{WorkerID: w.ID, RecipientSessionID: w.ChildSessionID.String}); err != nil {
			return nil, err
		}
	}
	rows, err := s.q.ListRoundInstructionsAfter(ctx, db.ListRoundInstructionsAfterParams{
		WorkerID: w.ID, RoundOrdinal: sql.NullInt64{Int64: ordinal, Valid: true}, AfterSeq: max(delivered, r.InstructionLastSeq.Int64),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, m := range rows {
		out = append(out, messageFromRow(m))
	}
	return out, nil
}
