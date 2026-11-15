package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	defaultWorkerHistoryLimit = 50
	maxWorkerHistoryLimit     = 100
)

var workerListLimit = httpio.MustPageLimit(100, 1, worker.MaxSessionPageSize)

func (s *Workers) handleListWorkers(w http.ResponseWriter, r *http.Request) {
	projectID, _, ok := requestscope.ProjectIDQuery(s.projectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	sessionID, present, err := httpio.SingleQueryValue(r, "session_id")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	if !present {
		s.responses.InvalidQueryParam(w, "session_id", "is required")
		return
	}
	status, _, err := httpio.SingleQueryValue(r, "status")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	if status != "" && !validWorkerStatus(wire.WorkerStatus(status)) {
		s.responses.InvalidQueryParam(w, "status", "must name a worker status")
		return
	}
	query, err := httpio.ReadPageQuery(r, workerListLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	page, err := s.workers.ListSessionPage(r.Context(), worker.SessionPageQuery{ProjectID: projectID, SessionID: sessionID, Status: wire.WorkerStatus(status), Cursor: query.Cursor, Limit: query.Limit})
	if errors.Is(err, pagecursor.ErrInvalid) || errors.Is(err, pagecursor.ErrExpired) {
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.WorkerListResponse{Workers: page.Workers, NextCursor: page.NextCursor})
}

func (s *Workers) handleWorkerHistory(w http.ResponseWriter, r *http.Request) {
	projectID, _, ok := requestscope.ProjectIDQuery(s.projectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	sessionID, target, hq, ok := s.readWorkerHistoryQuery(w, r)
	if !ok {
		return
	}
	if s.sessionStore != nil {
		sess, err := s.sessionStore.Get(r.Context(), sessionID)
		if err == nil && (sess == nil || sess.ProjectID != projectID) {
			err = store.ErrSessionNotFound
		}
		if err != nil {
			if errors.Is(err, store.ErrSessionNotFound) {
				s.responses.Fail(w, wire.ApiErrorCodeSessionNotFound, "session not found")
				return
			}
			s.responses.InternalError(w, r, err)
			return
		}
	}
	if s.workerOps == nil || s.workerOps.Reader.Records == nil {
		s.responses.Fail(w, wire.ApiErrorCodeWorkerNotFound, "worker not found")
		return
	}
	actor := workerrecords.Actor{SessionID: sessionID, Role: workerrecords.RoleCoordinator, PrincipalID: sessionID}
	resolved, err := s.resolveHistoryWorker(r.Context(), actor, target)
	if errors.Is(err, workerrecords.ErrWorkerNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeWorkerNotFound, "worker not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	page, err := s.workerOps.HistoryPaged(r.Context(), actor, resolved.Name, hq)
	if errors.Is(err, workerrecords.ErrWorkerNotFound) {
		s.responses.Fail(w, wire.ApiErrorCodeWorkerNotFound, "worker not found")
		return
	}
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	resp, err := s.projectWorkerHistory(r.Context(), actor, page)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, resp)
}

func (s *Workers) readWorkerHistoryQuery(w http.ResponseWriter, r *http.Request) (string, string, workerops.HistoryQuery, bool) {
	sessionID, present, err := httpio.SingleQueryValue(r, "session_id")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return "", "", workerops.HistoryQuery{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if !present || sessionID == "" {
		s.responses.InvalidQueryParam(w, "session_id", "is required")
		return "", "", workerops.HistoryQuery{}, false
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		s.responses.InvalidQueryParam(w, "session_id", "must be a valid UUID")
		return "", "", workerops.HistoryQuery{}, false
	}
	target, present, err := httpio.SingleQueryValue(r, "worker")
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return "", "", workerops.HistoryQuery{}, false
	}
	target = strings.TrimSpace(target)
	if !present || target == "" {
		s.responses.InvalidQueryParam(w, "worker", "is required")
		return "", "", workerops.HistoryQuery{}, false
	}
	before, ok := s.readPositiveIntQuery(w, r, "before", 0, 0)
	if !ok {
		return "", "", workerops.HistoryQuery{}, false
	}
	beforeRound, ok := s.readPositiveIntQuery(w, r, "before_round", 0, 0)
	if !ok {
		return "", "", workerops.HistoryQuery{}, false
	}
	limit, ok := s.readPositiveIntQuery(w, r, "limit", defaultWorkerHistoryLimit, maxWorkerHistoryLimit)
	if !ok {
		return "", "", workerops.HistoryQuery{}, false
	}
	return sessionID, target, workerops.HistoryQuery{BeforeSeq: before, BeforeRound: beforeRound, Limit: int(limit)}, true
}

func (s *Workers) readPositiveIntQuery(w http.ResponseWriter, r *http.Request, key string, fallback, maximum int64) (int64, bool) {
	raw, present, err := httpio.SingleQueryValue(r, key)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return 0, false
	}
	raw = strings.TrimSpace(raw)
	if !present || raw == "" {
		return fallback, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 || (maximum > 0 && v > maximum) {
		s.responses.InvalidQueryParam(w, key, "must be a positive integer within bounds")
		return 0, false
	}
	return v, true
}

func (s *Workers) resolveHistoryWorker(ctx context.Context, actor workerrecords.Actor, target string) (workerrecords.Worker, error) {
	records := s.workerOps.Reader.Records
	w, err := records.Resolve(ctx, actor, target)
	if !errors.Is(err, workerrecords.ErrWorkerNotFound) {
		return w, err
	}
	w, err = records.Worker(ctx, actor, target)
	if !errors.Is(err, workerrecords.ErrWorkerNotFound) {
		return w, err
	}
	round, found, rErr := records.RoundByJob(ctx, target)
	if rErr != nil {
		return workerrecords.Worker{}, rErr
	}
	if !found {
		return workerrecords.Worker{}, workerrecords.ErrWorkerNotFound
	}
	return records.Worker(ctx, actor, round.WorkerID)
}

func (s *Workers) projectWorkerHistory(ctx context.Context, actor workerrecords.Actor, page workerops.HistoryPage) (wire.WorkerHistoryResponse, error) {
	records := s.workerOps.Reader.Records
	disps, err := records.Dispositions(ctx, actor, page.Worker.ID)
	if err != nil {
		return wire.WorkerHistoryResponse{}, err
	}
	dispMap := make(map[string]workerrecords.Disposition, len(disps))
	for _, d := range disps {
		dispMap[d.ResultID] = d
	}
	coordDelivered, err := records.DeliveredThrough(ctx, actor, page.Worker.ID, actor.SessionID)
	if err != nil {
		return wire.WorkerHistoryResponse{}, err
	}
	workerDelivered, err := records.DeliveredThrough(ctx, actor, page.Worker.ID, page.Worker.ChildSessionID)
	if err != nil {
		return wire.WorkerHistoryResponse{}, err
	}
	roundsByOrd := make(map[int64]workerrecords.Round, len(page.Rounds))
	rounds := make([]wire.WorkerHistoryRound, 0, len(page.Rounds))
	for _, r := range page.Rounds {
		roundsByOrd[r.Ordinal] = r
		rounds = append(rounds, s.projectHistoryRound(r, dispMap))
	}
	messages := make([]wire.WorkerHistoryMessage, 0, len(page.Messages))
	for i := len(page.Messages) - 1; i >= 0; i-- {
		m := page.Messages[i]
		round, hasRound, rErr := lookupHistoryRound(ctx, records, actor, page.Worker.ID, m.RoundOrdinal, roundsByOrd)
		if rErr != nil {
			return wire.WorkerHistoryResponse{}, rErr
		}
		messages = append(messages, projectHistoryMessage(m, round, hasRound, coordDelivered, workerDelivered))
	}
	return wire.WorkerHistoryResponse{
		WorkerID:       page.Worker.ID,
		Name:           page.Worker.Name,
		AgentType:      page.Worker.AgentType,
		Brief:          page.Worker.Brief,
		ChildSessionID: page.Worker.ChildSessionID,
		State:          wire.WorkerState(page.State.State),
		Reason:         wire.WorkerStateReason(page.State.Reason),
		Rounds:         rounds,
		Messages:       messages,
		Before:         int(page.Before),
		BeforeRound:    int(page.BeforeRound),
	}, nil
}

func (s *Workers) projectHistoryRound(r workerrecords.Round, dispMap map[string]workerrecords.Disposition) wire.WorkerHistoryRound {
	out := wire.WorkerHistoryRound{
		Ordinal:             int(r.Ordinal),
		WorkerID:            r.JobID,
		InstructionFirstSeq: int(r.InstructionFirstSeq),
		InstructionLastSeq:  int(r.InstructionLastSeq),
		CheckpointID:        r.CheckpointID,
		CreatedAt:           r.CreatedAt,
		SettledAt:           r.SettledAt,
	}
	if subject, ok := r.Subject(); ok {
		out.ReviewOf = subject.ResultID
	}
	if r.SettledAt != "" && r.ResultID != "" {
		out.ResultHandle = r.ResultID
		d := dispMap[r.ResultID]
		out.ShelveReason = d.ShelveReason
		switch {
		case d.ChangeState != "":
			out.ResultDisposition = wire.WorkerResultDisposition(d.ChangeState)
		case r.CheckpointID != "":
			out.ResultDisposition = wire.WorkerResultOpen
		case d.AccountedAt != "":
			out.ResultDisposition = wire.WorkerResultAccounted
		default:
			out.ResultDisposition = wire.WorkerResultUnaccounted
		}
	}
	if s.workers != nil {
		if task, ok := s.workers.Get(r.JobID); ok && task != nil {
			out.Status = task.Status
			out.Result = task.Result
			out.Failure = task.Failure
		}
	}
	return out
}

func lookupHistoryRound(ctx context.Context, records *workerrecords.Store, actor workerrecords.Actor, workerID string, ordinal int64, cache map[int64]workerrecords.Round) (workerrecords.Round, bool, error) {
	if ordinal <= 0 {
		return workerrecords.Round{}, false, nil
	}
	if r, ok := cache[ordinal]; ok {
		return r, true, nil
	}
	rows, err := records.RoundsBefore(ctx, actor, workerID, ordinal+1, 1)
	if err != nil {
		return workerrecords.Round{}, false, err
	}
	if len(rows) == 0 || rows[0].Ordinal != ordinal {
		return workerrecords.Round{}, false, nil
	}
	cache[ordinal] = rows[0]
	return rows[0], true, nil
}

func projectHistoryMessage(m workerrecords.Message, round workerrecords.Round, hasRound bool, coordDelivered, workerDelivered int64) wire.WorkerHistoryMessage {
	out := wire.WorkerHistoryMessage{
		Seq:          int(m.Seq),
		SenderRole:   wire.WorkerMessageSenderRole(m.SenderRole),
		RoundOrdinal: int(m.RoundOrdinal),
		Body:         m.Body,
		ArtifactRef:  m.ArtifactRef,
		NeedsReply:   m.NeedsReply,
		ReplyToSeq:   int(m.ReplyToSeq),
		CreatedAt:    m.CreatedAt,
	}
	if m.SenderRole == workerrecords.RoleCoordinator {
		appliedThrough := workerDelivered
		if hasRound && round.InstructionLastSeq > appliedThrough {
			appliedThrough = round.InstructionLastSeq
		}
		switch {
		case m.Seq <= appliedThrough:
			out.Delivered = true
			out.InstructionStatus = wire.WorkerInstructionStatusApplied
		case hasRound && round.SettledAt == "":
			out.Delivered = false
			out.InstructionStatus = wire.WorkerInstructionStatusQueued
		default:
			out.Delivered = false
			out.InstructionStatus = wire.WorkerInstructionStatusNotApplied
		}
		return out
	}
	out.Delivered = m.Seq <= coordDelivered
	return out
}
