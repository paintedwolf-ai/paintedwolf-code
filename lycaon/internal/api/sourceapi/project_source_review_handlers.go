package sourceapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/storageusage"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) HandleCompleteProjectSourcePresentation(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	var req wire.SourcePresentationCompletion
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.FileID) == "" || strings.TrimSpace(req.EffectID) == "" || req.Ordinal <= 0 {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "file_id, effect_id, and ordinal are required")
		return
	}
	if err := s.SourceLedger.CompletePresentation(
		r.Context(), p.ID, req.FileID, req.EffectID, req.Ordinal,
	); err != nil {
		if errors.Is(err, sourceledger.ErrPresentationMismatch) {
			s.responses.Fail(w, wire.ApiErrorCodeSourcePresentationEffectChanged, "the presented file edit changed")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) HandleWithdrawProjectSourcePresentation(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	fileID := strings.TrimSpace(chi.URLParam(r, "file_id"))
	through, present, err := httpio.OptionalInt64Query(r, "through_ordinal", 1)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	if fileID == "" || !present {
		s.responses.InvalidQueryParam(w, "through_ordinal", "is required")
		return
	}
	err = s.SourceLedger.WithdrawPresentation(r.Context(), p.ID, fileID, through)
	switch {
	case errors.Is(err, sourceledger.ErrPresentationNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePresentationNotFound, "source presentation not found")
	case errors.Is(err, sourceledger.ErrPresentationMismatch):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePresentationEffectChanged,
			"a newer look at this file replaced the one being withdrawn")
	case err != nil:
		s.responses.InternalError(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Handler) HandleListProjectSourceSeen(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	p, ok = requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
	if !ok {
		return
	}
	pq, err := httpio.ReadPageQuery(r, sourceSeenPageLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	withoutUserEdits := false
	if mark, present, err := httpio.OptionalBoolQuery(r, "mark_user_edits"); err != nil {
		s.responses.InvalidQuery(w, err)
		return
	} else if present && !mark {
		withoutUserEdits = true
	}
	scope := pagecursor.Scope(p.ID, strings.TrimSpace(r.URL.Query().Get("session_id")), strconv.FormatBool(withoutUserEdits))
	query := sourceledger.SeenPageQuery{Limit: pq.Limit}
	if pq.Cursor != "" {
		cursor, err := sourceSeenPages.Decode(pq.Cursor, scope)
		if err == nil && (strings.TrimSpace(cursor.SeenTS) == "" || strings.TrimSpace(cursor.FileID) == "") {
			err = pagecursor.ErrInvalid
		}
		if err != nil {
			s.responses.PageCursorError(w, r, "cursor", err)
			return
		}
		query.AfterSeenTS, query.AfterFileID = cursor.SeenTS, cursor.FileID
	}
	res, err := s.SourceLedger.QuerySeen(r.Context(), p.ID, workspaceSourceBranches(p), query, withoutUserEdits)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := mapSourceSeen(res)
	if res.NextFileID != "" {
		out.NextCursor, err = sourceSeenPages.Encode(scope, sourceSeenCursor{SeenTS: res.NextSeenTS, FileID: res.NextFileID})
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

// sourceSeenCursor continues after one look, newest look first.
type sourceSeenCursor struct {
	SeenTS string `json:"seen_ts"`
	FileID string `json:"file_id"`
}

var (
	sourceSeenPages     = pagecursor.For[sourceSeenCursor]("source_seen")
	sourceSeenPageLimit = httpio.MustPageLimit(sourceledger.DefaultSeenFiles, 1, sourceledger.MaxSeenFiles)
)

func mapSourceSeen(res sourceledger.SeenResult) wire.SourceSeenList {
	out := wire.SourceSeenList{Files: make([]wire.SourceSeenFile, 0, len(res.Files))}
	for _, f := range res.Files {
		row := wire.SourceSeenFile{
			FileID: f.FileID, RootID: f.RootID, Path: f.Path,
			Tip:              wire.SourceTip{State: f.Tip.State, Sha256: f.Tip.SHA256},
			SeenAt:           f.SeenAt,
			ThroughOrdinal:   f.ThroughOrdinal,
			Effects:          make([]wire.SourceWalkEffect, 0, len(f.Effects)),
			EffectsTruncated: f.EffectsTruncated,
		}
		for _, effect := range f.Effects {
			row.Effects = append(row.Effects, MapSourceEffect(effect))
		}
		out.Files = append(out.Files, row)
	}
	return out
}

func (s *Handler) HandleGetProjectSourceStorage(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	inventory, err := s.sourceInventoryState(r.Context(), p)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	sourceLane, err := s.SourceLedger.StorageUsage(r.Context())
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	artifactLane := storageusage.Usage{Lane: storageusage.LaneArtifacts, Scope: storageusage.ScopeProject}
	if s.VisualStore != nil {
		artifactLane, err = s.VisualStore.StorageUsage(r.Context(), p.ID)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	attachmentLane := storageusage.Usage{Lane: storageusage.LanePromptAttachments, Scope: storageusage.ScopeProject}
	if _, available := s.AttachmentStore(r.Context(), p.ID); available {
		attachmentLane.UsedBytes, err = s.SessionStore.PromptAttachmentStorageUsage(r.Context(), p.ID)
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	httpio.WriteJSON(w, http.StatusOK, wire.SourceStorage{
		Inventory: mapSourceInventoryState(inventory, p.RootsGeneration),
		Storage:   storageUsageWire(storageusage.NewReport(artifactLane, sourceLane, attachmentLane)),
	})
}

func (s *Handler) HandleListProjectSourcePins(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	pq, err := httpio.ReadPageQuery(r, sourcePinPageLimit)
	if err != nil {
		s.responses.InvalidQuery(w, err)
		return
	}
	query, err := sourcePinPageQuery(pq, p.ID)
	if err != nil {
		s.responses.PageCursorError(w, r, "cursor", err)
		return
	}
	page, err := s.SourceLedger.ListPinsPage(r.Context(), p.ID, query)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	out := wire.SourcePinList{Pins: make([]wire.SourcePin, 0, len(page.Pins))}
	for _, pin := range page.Pins {
		out.Pins = append(out.Pins, mapSourcePin(pin))
	}
	if page.NextID != "" {
		out.NextCursor, err = sourcePinPages.Encode(pagecursor.Scope(p.ID), sourcePinCursor{
			CreatedTS: page.NextCreatedTS, ID: page.NextID,
		})
		if err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	httpio.WriteJSON(w, http.StatusOK, out)
}

type sourcePinCursor struct {
	CreatedTS string `json:"created_ts"`
	ID        string `json:"id"`
}

var (
	sourcePinPages     = pagecursor.For[sourcePinCursor]("source_pins")
	sourcePinPageLimit = httpio.MustPageLimit(sourceledger.DefaultPinPageLimit, 1, sourceledger.MaxPinPageLimit)
)

func sourcePinPageQuery(pq httpio.PageQuery, projectID string) (sourceledger.PinPageQuery, error) {
	query := sourceledger.PinPageQuery{Limit: pq.Limit}
	if pq.Cursor == "" {
		return query, nil
	}
	cursor, err := sourcePinPages.Decode(pq.Cursor, pagecursor.Scope(projectID))
	if err != nil {
		return query, err
	}
	if strings.TrimSpace(cursor.CreatedTS) == "" || strings.TrimSpace(cursor.ID) == "" {
		return query, pagecursor.ErrInvalid
	}
	query.BeforeCreatedTS, query.BeforeID = cursor.CreatedTS, cursor.ID
	return query, nil
}

func (s *Handler) HandleCreateProjectSourcePin(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	var req wire.SourcePinCreate
	if err := httpio.DecodeJSON(w, r, &req); err != nil && !errors.Is(err, io.EOF) {
		s.responses.DecodeError(w, r, err)
		return
	}
	inventory, err := s.sourceInventoryState(r.Context(), p)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	if !inventory.Complete {
		s.responses.Fail(w, wire.ApiErrorCodeSourceInventoryPending, "source inventory is still being prepared")
		return
	}
	pin, err := s.SourceLedger.CreatePin(r.Context(), p.ID, strings.TrimSpace(req.Label))
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, mapSourcePin(pin))
}

func mapSourceInventoryState(state sourceledger.InventoryState, rootsGeneration int) wire.SourceInventoryState {
	out := wire.SourceInventoryState{
		Status: string(state.Phase), Complete: state.Complete,
		RootsGeneration: rootsGeneration, IndexedFiles: state.FileCount,
		SourceSnapshotID: state.SnapshotID, WorktreeEpoch: state.EpochToken,
	}
	if !state.StartedAt.IsZero() {
		started := state.StartedAt
		out.StartedAt = &started
	}
	if !state.CompletedAt.IsZero() {
		completed := state.CompletedAt
		out.CompletedAt = &completed
	}
	return out
}

func (s *Handler) HandleUpdateProjectSourcePin(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	var req wire.SourcePinUpdate
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	pinID := chi.URLParam(r, "pin_id")
	if req.Label != nil {
		if err := s.SourceLedger.UpdatePinLabel(
			r.Context(), p.ID, pinID, strings.TrimSpace(*req.Label),
		); err != nil {
			if errors.Is(err, sourceledger.ErrPinNotFound) {
				s.responses.Fail(w, wire.ApiErrorCodeSourcePinNotFound, "pin not found")
				return
			}
			s.responses.InternalError(w, r, err)
			return
		}
	}
	pin, err := s.SourceLedger.GetPin(r.Context(), p.ID, pinID)
	if err != nil {
		if errors.Is(err, sourceledger.ErrPinNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSourcePinNotFound, "pin not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, mapSourcePin(pin))
}

func (s *Handler) HandleDeleteProjectSourcePin(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return
	}
	if err := s.SourceLedger.DeletePin(r.Context(), p.ID, chi.URLParam(r, "pin_id")); err != nil {
		if errors.Is(err, sourceledger.ErrPinNotFound) {
			s.responses.Fail(w, wire.ApiErrorCodeSourcePinNotFound, "pin not found")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func mapSourcePin(pin sourceledger.Pin) wire.SourcePin {
	out := wire.SourcePin{
		ID: pin.ID, ProjectID: pin.ProjectID, Label: pin.Label, CreatedAt: pin.CreatedTS,
	}
	for _, head := range pin.GitHeads {
		out.GitHeads = append(out.GitHeads, wire.SourcePinGitHead{
			RootID: head.RootID, RepoState: head.RepoState,
			HeadCommit: head.HeadCommit, HeadRef: head.HeadRef,
		})
	}
	return out
}

func storageUsageWire(report storageusage.Report) wire.StorageUsageReport {
	out := wire.StorageUsageReport{
		Lanes: make([]wire.StorageUsageLane, 0, len(report.Lanes)),
	}
	for _, lane := range report.Lanes {
		out.Lanes = append(out.Lanes, wire.StorageUsageLane{
			Lane:      string(lane.Lane),
			Scope:     string(lane.Scope),
			UsedBytes: lane.UsedBytes,
		})
	}
	return out
}
