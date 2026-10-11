package editoradmin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/textfile"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) editorProject(w http.ResponseWriter, r *http.Request) (*project.Project, bool) {
	p, ok := requestscope.ProjectByURLID(s.ProjectRegistry, s.responses, w, r)
	if !ok {
		return nil, false
	}
	if id := chi.URLParam(r, "document_id"); id != "" {
		scoped, err := s.EditorDocuments.Workspace(r.Context(), p, id)
		if err != nil {
			s.writeEditorDocumentError(w, r, err)
			return nil, false
		}
		return scoped, true
	}
	return requestscope.ProjectForRequest(s.SessionStore, s.responses, w, r, p)
}

func (s *Handler) HandleOpenEditorDocument(w http.ResponseWriter, r *http.Request) {
	var req wire.OpenEditorDocumentRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	switch {
	case strings.TrimSpace(req.Path) == "":
		s.responses.InvalidField(w, "path", "is required")
		return
	case uuid.Validate(req.RootID) != nil:
		s.responses.InvalidField(w, "root_id", "must be a UUID")
		return
	case strings.TrimSpace(req.ClientID) == "":
		s.responses.InvalidField(w, "client_id", "is required")
		return
	}
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	d, err := s.EditorDocuments.Open(r.Context(), p, req.Path, req.RootID, req.DecodeAs, req.ClientID, retainedReplica(req.Replica))
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	d.WorkspaceID = p.WorkspaceID()
	document := s.editorDocumentDTO(r.Context(), d)
	if retained := req.Replica; retained != nil && retained.DocumentID == document.ID && retained.Epoch == document.Epoch && retained.BaseSHA256 == document.BaseSHA256 {
		document.BaseContent = nil
	}
	httpio.WriteJSON(w, http.StatusCreated, document)
}

func retainedReplica(replica *wire.RetainedReplica) *editordoc.Retained {
	if replica == nil {
		return nil
	}
	return &editordoc.Retained{DocumentID: replica.DocumentID, Epoch: replica.Epoch, Vector: replica.StateVector}
}

func (s *Handler) HandleReplaceEditorDocument(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.ReplaceEditorDocumentRequest
	if err := httpio.DecodeSourceJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID, turn, ok := s.editorAffiliation(w, r, p.ID, req.SessionID)
	if !ok {
		return
	}
	d, err := s.EditorDocuments.ReplaceSnapshot(r.Context(), chi.URLParam(r, "document_id"), p.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{HistoryVector: req.HistoryVector, SessionID: sessionID, Turn: turn, ClientID: req.ClientID, OperationID: req.OperationID, ExpectedRevision: req.ExpectedRevision}, Content: req.Content, EOL: req.EOL, MixedEOL: req.MixedEOL})
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.editorDocumentDTO(r.Context(), d))
}

func (s *Handler) HandleSyncEditorDocument(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()

	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.SyncEditorDocumentRequest
	if err := httpio.DecodeSourceJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	d, err := s.EditorDocuments.Join(r.Context(), chi.URLParam(r, "document_id"), p.ID, editordoc.ReplicaJoin{ClientID: req.ClientID, Incarnation: req.Incarnation, Epoch: req.Epoch, Vector: req.StateVector})
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.editorReplicaFrameDTO(r.Context(), d, req.BaseSHA256))
}

func (s *Handler) HandleSubmitEditorDocumentUpdate(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()

	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.SubmitEditorDocumentUpdateRequest
	if err := httpio.DecodeSourceJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID, turn, ok := s.editorAffiliation(w, r, p.ID, &req.SessionID)
	if !ok {
		return
	}
	result, err := s.EditorDocuments.SubmitReplica(r.Context(), p.ID, chi.URLParam(r, "document_id"), editordoc.ReplicaSubmission{
		SessionID: sessionID, Turn: turn,
		ClientID: req.ClientID, ReplicaID: req.ReplicaID, OperationID: req.OperationID, Epoch: req.Epoch, Update: req.Update, Vector: req.StateVector})
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	dto := s.editorReplicaFrameDTO(r.Context(), result.Document, req.BaseSHA256)
	dto.AcceptedRevision = result.AcceptedRevision
	dto.AcceptedOperationID = req.OperationID
	httpio.WriteJSON(w, http.StatusOK, dto)
}

func (s *Handler) HandlePublishEditorDocumentPresence(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()

	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.UpdateEditorDocumentPresenceRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if err := s.EditorDocuments.UpdatePresence(r.Context(), chi.URLParam(r, "document_id"), p.ID, editorPresenceInput(req)); err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) HandleLeaveEditorDocument(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(chi.URLParam(r, "id"))
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, projectID)
	if release == nil {
		return
	}
	retryPromotion := false
	defer func(ctx context.Context) {
		release()
		if retryPromotion {
			s.TryRunPromotion(ctx, projectID)
		}
	}(context.WithoutCancel(r.Context()))
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.LeaveEditorDocumentRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	err := s.EditorDocuments.Leave(r.Context(), chi.URLParam(r, "document_id"), p.ID, req.ClientID, req.Incarnation)
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	retryPromotion = true
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) HandleDiscardEditorDocument(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.EditorDocumentCommandRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID, turn, ok := s.editorAffiliation(w, r, p.ID, &req.SessionID)
	if !ok {
		return
	}
	d, err := s.EditorDocuments.Discard(r.Context(), chi.URLParam(r, "document_id"), p.ID, editordoc.DocumentCommand{HistoryVector: req.HistoryVector, SessionID: sessionID, Turn: turn, ClientID: req.ClientID, OperationID: req.OperationID, ExpectedRevision: req.ExpectedRevision})
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.editorDocumentDTO(r.Context(), d))
}

func (s *Handler) HandleReloadEditorDocument(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.EditorDocumentCommandRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID, turn, ok := s.editorAffiliation(w, r, p.ID, &req.SessionID)
	if !ok {
		return
	}
	d, err := s.EditorDocuments.Reload(r.Context(), p, chi.URLParam(r, "document_id"), editordoc.DocumentCommand{HistoryVector: req.HistoryVector, SessionID: sessionID, Turn: turn, ClientID: req.ClientID, OperationID: req.OperationID, ExpectedRevision: req.ExpectedRevision})
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.editorDocumentDTO(r.Context(), d))
}

func (s *Handler) HandleObserveEditorDocument(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.ObserveEditorDocumentRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	d, err := s.EditorDocuments.ObserveDisk(r.Context(), p, chi.URLParam(r, "document_id"), req.ClientID)
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.editorDocumentDTO(r.Context(), d))
}

func (s *Handler) HandleSaveEditorDocument(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()
	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.SaveEditorDocumentRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID, turn, ok := s.editorAffiliation(w, r, p.ID, &req.SessionID)
	if !ok {
		return
	}
	if _, err := uuid.Parse(req.OperationID); err != nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	d, err := s.EditorDocuments.Save(r.Context(), p, chi.URLParam(r, "document_id"), req.ClientID, req.OperationID, sessionID, turn, req.ExpectedRevision)
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.editorDocumentDTO(r.Context(), d))
}

type editorDocumentPresentation struct {
	screenStatus wire.SecretScreenStatus
	screen       *wire.SecretScreen
}

func (s *Handler) projectEditorDocumentPresentation(ctx context.Context, d *editordoc.Document) editorDocumentPresentation {
	status, screen := s.EditorScreenProjection(ctx, d)
	return editorDocumentPresentation{
		screenStatus: status,
		screen:       screen,
	}
}

// heldAgentVersionID identifies recoverable agent edits absent from disk.
func heldAgentVersionID(d *editordoc.Document) *string {
	if d == nil || strings.TrimSpace(d.HeldAgentVersionID) == "" {
		return nil
	}
	id := d.HeldAgentVersionID
	return &id
}

func editorBaseContent(d *editordoc.Document) *string {
	if d == nil || d.BaseContent == d.Draft {
		return nil
	}
	base := d.BaseContent
	return &base
}

func (s *Handler) editorDocumentDTO(ctx context.Context, d *editordoc.Document) wire.EditorDocument {
	presentation := s.projectEditorDocumentPresentation(ctx, d)
	return wire.EditorDocument{
		SaveRevision:   d.SaveRevision,
		CommandHistory: editorCommandHistoryDTO(d.CommandHistory),
		ID:             d.ID, ProjectID: d.ProjectID, WorkspaceID: d.WorkspaceID, FileID: d.FileID,
		ReplicaID: d.ReplicaID, RootID: d.RootID, Path: d.Path, BaseSHA256: d.BaseSHA256,
		Encoding: wire.SourceEncoding(d.Encoding), SizeBytes: d.SizeBytes,
		EOL: d.EOL, BaseEOL: d.BaseEOL, MixedEOL: d.MixedEOL,
		BaseMixedEOL: d.BaseMixedEOL, Revision: d.Revision, Dirty: d.Dirty,
		Diverged: d.Diverged, Absent: d.Absent, HeldAgentVersionID: heldAgentVersionID(d),
		Epoch: d.Epoch, StateVector: d.StateVector, CRDTUpdate: d.CRDTUpdate, PublishedRevision: d.PublishedRevision, Participants: editorParticipants(d),
		SecretScreenStatus: presentation.screenStatus,
		BaseContent:        editorBaseContent(d), SecretScreen: presentation.screen,
	}
}

func (s *Handler) editorReplicaFrameDTO(ctx context.Context, d *editordoc.Document, baseSHA256 string) wire.EditorReplicaFrame {
	presentation := s.projectEditorDocumentPresentation(ctx, d)
	var base *string
	if baseSHA256 != d.BaseSHA256 {
		content := d.BaseContent
		base = &content
	}
	return wire.EditorReplicaFrame{
		ID: d.ID, ProjectID: d.ProjectID, WorkspaceID: d.WorkspaceID, FileID: d.FileID,
		ReplicaID: d.ReplicaID, RootID: d.RootID, Path: d.Path, BaseSHA256: d.BaseSHA256,
		Encoding: wire.SourceEncoding(d.Encoding), SizeBytes: d.SizeBytes,
		EOL: d.EOL, BaseEOL: d.BaseEOL, MixedEOL: d.MixedEOL,
		BaseMixedEOL: d.BaseMixedEOL, Revision: d.Revision, Dirty: d.Dirty,
		Diverged: d.Diverged, Absent: d.Absent, HeldAgentVersionID: heldAgentVersionID(d),
		Epoch: d.Epoch, StateVector: d.StateVector, CRDTUpdate: d.CRDTUpdate, PublishedRevision: d.PublishedRevision, Participants: editorParticipants(d),
		SecretScreenStatus: presentation.screenStatus,
		BaseContent:        base, SecretScreen: presentation.screen,
	}
}

func (s *Handler) EditorDocumentEventDTO(ctx context.Context, d *editordoc.Document, contentChanged bool) wire.EditorDocumentEvent {
	presentation := s.projectEditorDocumentPresentation(ctx, d)
	event := wire.EditorDocumentEvent{
		ID: d.ID, ProjectID: d.ProjectID, WorkspaceID: d.WorkspaceID, FileID: d.FileID,
		RootID: d.RootID, Path: d.Path, BaseSHA256: d.BaseSHA256,
		Encoding: wire.SourceEncoding(d.Encoding), SizeBytes: d.SizeBytes,
		EOL: d.EOL, BaseEOL: d.BaseEOL, MixedEOL: d.MixedEOL,
		BaseMixedEOL: d.BaseMixedEOL, Revision: d.Revision, Dirty: d.Dirty,
		Diverged: d.Diverged, Absent: d.Absent, HeldAgentVersionID: heldAgentVersionID(d),
		Epoch: d.Epoch, PublishedRevision: d.PublishedRevision, Participants: editorParticipants(d),
		SecretScreenStatus: presentation.screenStatus,
		ContentChanged:     contentChanged, SecretScreen: presentation.screen,
	}

	return event
}

func (s *Handler) writeEditorDocumentError(w http.ResponseWriter, r *http.Request, err error) {
	var rejected *documentcore.Rejected
	if errors.As(err, &rejected) {
		s.responses.FailDetails(w, wire.ApiErrorCodeInvalidRequest,
			map[string]any{"reject_code": rejected.Code, "reason": "the document update could not be accepted"},
			"the document update could not be accepted")
		return
	}
	if httpio.WriteSourceEncodingError(s.responses, w, err) {
		return
	}
	switch {
	case errors.Is(err, projectsource.ErrSourceBusy):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePathBusy, "another file operation is using this path")
	case errors.Is(err, editordoc.ErrInvalidEOL):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "line ending must be lf or crlf")
	case errors.Is(err, textfile.ErrRawTooLarge), errors.Is(err, textfile.ErrTextTooLarge), errors.Is(err, projectsource.ErrSourceWriteTooLarge):
		s.responses.Fail(w, wire.ApiErrorCodeSourceContentTooLarge, "content exceeds the 4 MiB editor cap")
	case errors.Is(err, textfile.ErrBinary), errors.Is(err, textfile.ErrUnsupported):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "content must be valid text in the document encoding")
	case errors.Is(err, editordoc.ErrNotFound), errors.Is(err, projectsource.ErrSourceNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeEditorDocumentNotFound, "editor document not found")
	case errors.Is(err, editordoc.ErrOperationConflict):
		s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "operation_id was already used for different editor save input")
	case errors.Is(err, editordoc.ErrRevisionConflict), errors.Is(err, projectsource.ErrSourceWriteConflict):
		s.responses.Fail(w, wire.ApiErrorCodeEditorRevisionConflict, "the editor document or file changed")
	case errors.Is(err, editordoc.ErrReplicaEpoch):
		s.responses.Fail(w, wire.ApiErrorCodeEditorReplicaEpoch, "the editor document identity changed before synchronization")
	case errors.Is(err, editordoc.ErrReplicaIdentity):
		s.responses.Fail(w, wire.ApiErrorCodeEditorReplicaIdentity, "the editor replica must synchronize before submitting changes")
	case errors.Is(err, editordoc.ErrRootDetached):
		s.responses.Fail(w, wire.ApiErrorCodeEditorRootDetached, "the file's root is no longer attached to this project")
	case errors.Is(err, projectsource.ErrSourcePathDenied):
		s.responses.Fail(w, wire.ApiErrorCodeSourcePathDenied, "source path is outside the project")
	case errors.Is(err, editordoc.ErrReadOnly):
		s.responses.Fail(w, wire.ApiErrorCodeSourceReadOnly, "the file is read-only")
	case errors.Is(err, projectsource.ErrSourceBinary):
		s.responses.Fail(w, wire.ApiErrorCodeSourceBinary, "only editable text files can be opened as documents")
	default:
		s.responses.InternalError(w, r, err)
	}
}

func editorParticipants(d *editordoc.Document) []wire.EditorParticipant {
	out := make([]wire.EditorParticipant, 0, len(d.Participants))
	for _, p := range d.Participants {
		ranges := make([]wire.EditorPresenceRange, 0, len(p.Ranges))
		for _, selected := range p.Ranges {
			ranges = append(ranges, wire.EditorPresenceRange{Anchor: selected.Anchor, Head: selected.Head})
		}
		out = append(out, wire.EditorParticipant{ClientID: p.ClientID, PersonID: p.PersonID, WindowNumber: p.WindowNumber, Ranges: ranges, Main: p.Main})
	}
	return out
}

func (s *Handler) HandleCreateEditorDocumentSnapshot(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()

	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.PinEditorDocumentRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	var d *editordoc.Document
	var err error
	if req.OperationID != "" {
		d, err = s.EditorDocuments.PinSave(r.Context(), p.ID, chi.URLParam(r, "document_id"), req.ClientID, req.OperationID)
	} else {
		d, err = s.EditorDocuments.Pin(r.Context(), p.ID, chi.URLParam(r, "document_id"), 0)
	}
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, s.editorDocumentDTO(r.Context(), d))
}

func (s *Handler) HandleResolveEditorDocumentConflict(w http.ResponseWriter, r *http.Request) {
	release := requestscope.BeginRuntime(s.MutationGate, s.responses, w, r, strings.TrimSpace(chi.URLParam(r, "id")))
	if release == nil {
		return
	}
	defer release()

	p, ok := s.editorProject(w, r)
	if !ok {
		return
	}
	var req wire.ResolveEditorDocumentRequest
	if err := httpio.DecodeSourceJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	sessionID, turn, ok := s.editorAffiliation(w, r, p.ID, &req.SessionID)
	if !ok {
		return
	}
	d, err := s.EditorDocuments.Resolve(r.Context(), p, chi.URLParam(r, "document_id"), editordoc.ConflictResolution{HistoryVector: req.HistoryVector,
		ClientID: req.ClientID, OperationID: req.OperationID, ExpectedRevision: req.ExpectedRevision,
		DiskSHA256: req.DiskSHA256, Content: req.Content, EOL: req.EOL, SessionID: sessionID, Turn: turn})
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, s.editorDocumentDTO(r.Context(), d))
}

func editorPresenceInput(req wire.UpdateEditorDocumentPresenceRequest) editordoc.Participant {
	out := editordoc.Participant{ClientID: req.ClientID, Incarnation: req.Incarnation, Main: req.Main}
	for _, selected := range req.Ranges {
		out.Ranges = append(out.Ranges, editordoc.PresenceRange{Anchor: selected.Anchor, Head: selected.Head})
	}
	return out
}
