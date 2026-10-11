package promptadmin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/recovery"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Submission) HandlePrompt(w http.ResponseWriter, r *http.Request) {
	perf := observability.StartPerformanceOperation("prompt.admit", nil)
	outcome := "rejected"
	defer func() { perf.End(outcome) }()
	id := chi.URLParam(r, "id")
	var req wire.PromptRequest
	// The limit bounds prose and attachment handles; attachment bytes use a separate request.
	if err := httpio.DecodeJSONLimit(w, r, &req, s.Caps.Transport.MaxPromptRequest.Int64()); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	parsedOperationID, err := uuid.Parse(strings.TrimSpace(req.OperationID))
	if err != nil {
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "operation_id must be a UUID")
		return
	}
	req.OperationID = parsedOperationID.String()
	perf.SetDimension("session_id", id)
	perf.SetDimension("operation_id", req.OperationID)
	unlockOperation := s.Sessions.Submissions.LockOperation(req.OperationID)
	defer unlockOperation()
	if len(req.Text) > s.Caps.Composer.MaxInlineText.Int() {
		s.responses.Fail(w, wire.ApiErrorCodePromptTextTooLarge, "inline prompt text exceeds the configured limit")
		return
	}
	replayed, found, err := s.Sessions.Submissions.ReplayPromptSubmission(r.Context(), id, req.OperationID, req)
	if err != nil {
		var conflict *store.PromptSubmissionConflictError
		if errors.As(err, &conflict) {
			s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "this operation_id was already used for a different prompt")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	if found {
		revision := s.EventPublisher.NextSessionRevision()
		s.Execution.ResumePromptSubmission(r.Context(), id, replayed)
		httpio.WriteJSON(w, http.StatusAccepted, promptAccepted(replayed, revision))
		perf.SetDimension("replayed", "true")
		outcome = "accepted"
		return
	}
	perf.Mark("decode_replay")
	text := strings.TrimSpace(req.Text)
	userProse := text
	caps := s.Caps
	if err := promptattach.EnforceCounts(caps, len(req.Attachments), len(req.References)); err != nil {
		s.WriteAttachmentError(w, err)
		return
	}

	sess, ok := s.promptSessionForAdmission(w, r, id)
	if !ok {
		return
	}
	allowEmpty := s.Sessions.Runner.AcceptsEmpty(r.Context(), id)
	if req.Recovery == nil && text == "" && len(req.Attachments) == 0 && len(req.References) == 0 && len(req.Secrets) == 0 && !allowEmpty {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "text, attachments, references, or an active workflow request required")
		return
	}

	attachStore, ok := s.Attachments.AttachmentStore(r.Context(), sess.ProjectID)
	if !ok && len(req.Attachments) > 0 {
		s.responses.Unavailable(w, wire.ApiErrorCodeAttachmentUnavailable, "attachment storage is not configured for this project")
		return
	}
	prepared, ok := s.preparePromptAdmission(w, r, id, sess, req, attachStore, text, userProse, allowEmpty)
	if !ok {
		return
	}
	perf.Mark("prepare_attachments")
	vision := s.Sessions.Coordinator.Model.Vision(r.Context(), sess)
	acquired, err := attachStore.Retain(req.OperationID, prepared.retainedBlobIDs)
	if err != nil {
		s.discardPromptImages(r.Context(), sess.ProjectID, prepared.createdArtifactIDs)
		s.responses.InternalError(w, r, err)
		return
	}

	row, _, err := s.Sessions.Submissions.AdmitPrompt(r.Context(), id, req.OperationID, req, prepared.input)
	if err != nil {
		if releaseErr := attachStore.Release(req.OperationID, acquired); releaseErr != nil && s.responses.Logger != nil {
			s.responses.Logger.WarnContext(r.Context(), "release unadmitted prompt attachments", "operation_id", req.OperationID, "error", releaseErr)
		}
		s.discardPromptImages(r.Context(), sess.ProjectID, prepared.createdArtifactIDs)
		if errors.Is(err, spendguard.ErrCeiling) {
			s.responses.FailDetails(w, wire.ApiErrorCodeSessionSpendCeilingReached, usernotice.SpendCeilingContext(err), "chat spend ceiling reached")
			return
		}
		if errors.Is(err, recovery.ErrInvalid) {
			s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, err.Error())
			return
		}
		var stale *recovery.StaleError
		if errors.As(err, &stale) {
			s.responses.FailDetails(w, wire.ApiErrorCodePromptRecoveryStale,
				map[string]any{"reason": stale.Reason, "explanation": stale.Message}, stale.Message)
			return
		}
		var conflict *store.PromptSubmissionConflictError
		if errors.As(err, &conflict) {
			s.responses.Fail(w, wire.ApiErrorCodeIdempotencyConflict, "this operation_id was already used for a different prompt")
			return
		}
		s.responses.InternalError(w, r, err)
		return
	}
	perf.Mark("persist")
	if releaseErr := attachStore.Release(req.OperationID, acquired); releaseErr != nil && s.responses.Logger != nil {
		s.responses.Logger.WarnContext(r.Context(), "release admitted prompt attachment guard", "operation_id", req.OperationID, "error", releaseErr)
	}
	if prepared.hasImages && !vision {
		s.publishUserImageNotVisible(r.Context(), id, sess)
	}
	if prepared.scannedNoText {
		s.publishAttachmentNotice(r.Context(), id, sess, ErrAttachmentScannedNoText)
	}
	revision := s.EventPublisher.NextSessionRevision()
	s.Execution.ResumePromptSubmission(r.Context(), id, row)
	httpio.WriteJSON(w, http.StatusAccepted, promptAccepted(row, revision))
	perf.Mark("dispatch")
	outcome = "accepted"
}

// promptAccepted carries a session revision minted after the receipt is
// durable; every later session event reflects the receipt.
func promptAccepted(row *store.PromptSubmission, revision uint64) wire.PromptAcceptedResponse {
	sessionRevision := int64(math.MaxInt64)
	if revision <= math.MaxInt64 {
		sessionRevision = int64(revision)
	}
	return wire.PromptAcceptedResponse{
		Status:          string(row.Status),
		OperationID:     row.ID,
		MessageID:       row.ID,
		SessionRevision: sessionRevision,
	}
}

type promptAdmission struct {
	input              promptinput.Input
	createdArtifactIDs []string
	retainedBlobIDs    []string
	hasImages          bool
	scannedNoText      bool
}

func (s *Submission) preparePromptAdmission(
	w http.ResponseWriter,
	r *http.Request,
	sessionID string,
	sess *wire.Session,
	req wire.PromptRequest,
	attachStore blobstore.Store,
	text string,
	userProse string,
	allowEmpty bool,
) (promptAdmission, bool) {
	if req.Recovery != nil {
		if len(req.Attachments) != 0 || len(req.References) != 0 || len(req.Secrets) != 0 {
			s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "Recovery cannot add attachments, references, or secrets; send them as a new message")
			return promptAdmission{}, false
		}
		return promptAdmission{input: promptinput.Input{Text: text, Recovery: req.Recovery}}, true
	}

	caps := s.Caps
	previewBudget := promptattach.NewTurnPreviewBudget(caps)
	ingested, err := promptattach.IngestAttachments(r.Context(), attachStore, caps, previewBudget, s.Video, req.Attachments)
	if err != nil {
		s.WriteAttachmentError(w, err)
		return promptAdmission{}, false
	}
	if len(ingested.Images) > caps.Counts.MaxImages {
		s.responses.Fail(w, wire.ApiErrorCodeAttachmentTooLarge, fmt.Sprintf("at most %d images allowed", caps.Counts.MaxImages))
		return promptAdmission{}, false
	}
	text = promptattach.JoinUserText(text, ingested.Fences())
	refResult, err := promptattach.IngestReferences(s.References.ReferenceDeps(r.Context(), sess), previewBudget, req.References)
	if err != nil {
		s.WriteAttachmentError(w, err)
		return promptAdmission{}, false
	}
	text = promptattach.JoinUserText(text, refResult.Fences())
	secretFence, secretParts, err := s.ingestPromptSecrets(r.Context(), sess, req.Secrets)
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return promptAdmission{}, false
	}
	if secretFence != "" {
		text = promptattach.JoinUserText(text, []string{secretFence})
	}
	if len(ingested.Images)+len(refResult.ArtifactIDs) > caps.Counts.MaxImages {
		s.responses.Fail(w, wire.ApiErrorCodeAttachmentTooLarge, fmt.Sprintf("at most %d images allowed", caps.Counts.MaxImages))
		return promptAdmission{}, false
	}
	if text == "" && len(ingested.Images) == 0 && len(refResult.ArtifactIDs) == 0 && !allowEmpty {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "text, attachments, or references required")
		return promptAdmission{}, false
	}
	createdArtifactIDs, err := s.ingestPromptImages(r.Context(), sessionID, req.OperationID, "attached", ingested.Images)
	if err != nil {
		s.discardPromptImages(r.Context(), sess.ProjectID, createdArtifactIDs)
		s.WriteAttachmentError(w, err)
		return promptAdmission{}, false
	}
	artifactIDs := append([]string(nil), createdArtifactIDs...)
	artifactIDs = append(artifactIDs, refResult.ArtifactIDs...)
	retainedBlobIDs := make([]string, 0, len(ingested.Parts))
	for _, part := range ingested.Parts {
		if part.BlobID != "" {
			retainedBlobIDs = append(retainedBlobIDs, part.BlobID)
		}
	}
	return promptAdmission{
		input: promptinput.Input{
			Text: text, ArtifactIDs: artifactIDs, Recovery: req.Recovery,
			SourceContext: refResult.SourceContext(),
			ContentParts:  PromptContentParts(userProse, ingested.Parts, refResult.Parts, secretParts),
		},
		createdArtifactIDs: createdArtifactIDs,
		retainedBlobIDs:    retainedBlobIDs,
		hasImages:          len(ingested.Images) > 0 || len(refResult.ArtifactIDs) > 0,
		scannedNoText:      ingested.ScannedNoText,
	}, true
}

func (s *Submission) promptSessionForAdmission(w http.ResponseWriter, r *http.Request, id string) (*wire.Session, bool) {
	sess, ok := requestscope.Session(s.Store, s.responses, w, r, id)
	if !ok {
		return nil, false
	}
	if sess.Status == wire.SessionStatusPreparing {
		s.responses.Fail(w, wire.ApiErrorCodeSessionPreparing, "session workspace is still preparing")
		return nil, false
	}
	if unlock, locked := s.Sessions.Runner.Execution.TryIdleMutation(id); locked {
		err := s.Sessions.Workspace.CheckReady(r.Context(), id)
		unlock()
		if errors.Is(err, sessionscope.ErrWorktreeStale) {
			s.responses.Fail(w, wire.ApiErrorCodeWorktreeStale, "This chat's worktree is missing. Unbind it in the Git tab to continue.")
			return nil, false
		}
		if err != nil {
			s.responses.InternalError(w, r, err)
			return nil, false
		}
	}
	return sess, true
}

// PromptContentParts preserves origin metadata for attached content.
func PromptContentParts(prose string, attachments, references []promptattach.FramedPart, secrets []wire.MessageContentPart) []wire.MessageContentPart {
	if len(attachments) == 0 && len(references) == 0 && len(secrets) == 0 {
		return nil
	}
	parts := make([]wire.MessageContentPart, 0, 2+len(attachments)+len(references))
	if prose = strings.TrimSpace(prose); prose != "" {
		parts = append(parts, wire.MessageContentPart{
			Content:   prose,
			Origin:    wire.MessageOriginUser,
			Authority: wire.ContentAuthorityUser, TrustTier: wire.ContentTrustTierTrusted,
		})
	}
	sources := make([]string, 0, len(attachments)+len(references))
	for _, framed := range attachments {
		sources = append(sources, framed.Source)
	}
	for _, framed := range references {
		sources = append(sources, framed.Source)
	}
	parts = append(parts, wire.MessageContentPart{
		Content:   promptattach.SubjectBindingNotice(sources),
		Origin:    wire.MessageOriginHost,
		Authority: wire.ContentAuthoritySystem,
		TrustTier: wire.ContentTrustTierTrusted,
		Source:    promptattach.SubjectBindingSource,
	})
	for _, framed := range attachments {
		parts = append(parts, wire.MessageContentPart{
			Content:   strings.TrimSpace(framed.Fence),
			Origin:    wire.MessageOriginAttachment,
			Authority: wire.ContentAuthorityNone,
			TrustTier: wire.ContentTrustTierUntrusted,
			Source:    strings.TrimSpace(framed.Source),
			MediaType: strings.TrimSpace(framed.MediaType),
			BlobID:    strings.TrimSpace(framed.BlobID),
			Path:      strings.TrimSpace(framed.Path),
			SizeBytes: framed.SizeBytes,
		})
	}
	for _, framed := range references {
		parts = append(parts, wire.MessageContentPart{
			Content:         strings.TrimSpace(framed.Fence),
			Origin:          wire.MessageOriginRetrieval,
			Authority:       wire.ContentAuthorityNone,
			TrustTier:       wire.ContentTrustTierUntrusted,
			Source:          strings.TrimSpace(framed.Source),
			MediaType:       strings.TrimSpace(framed.MediaType),
			Path:            strings.TrimSpace(framed.Path),
			RootID:          strings.TrimSpace(framed.RootID),
			SizeBytes:       framed.SizeBytes,
			ReferenceKind:   framed.ReferenceKind,
			HitKind:         strings.TrimSpace(framed.HitKind),
			SourceRef:       strings.TrimSpace(framed.SourceRef),
			SourceSessionID: strings.TrimSpace(framed.SourceSessionID),
			StartLine:       framed.StartLine,
			EndLine:         framed.EndLine,
		})
	}
	parts = append(parts, secrets...)
	return parts
}

func (s *Submission) WriteAttachmentError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch attacherr.CodeOf(err) {
	case attacherr.CodeUnsupported:
		s.responses.Fail(w, wire.ApiErrorCodeUnsupportedAttachment, "the attachment type is not supported")
	case attacherr.CodeTooLarge:
		s.responses.Fail(w, wire.ApiErrorCodeAttachmentTooLarge, "the attachment is too large")
	case attacherr.CodeOutOfJail:
		s.responses.Fail(w, wire.ApiErrorCodeReferenceOutOfJail, "the referenced file is outside the project")
	case attacherr.CodeNotFound:
		s.responses.Fail(w, wire.ApiErrorCodeAttachmentNotFound, "the attachment was not found")
	case attacherr.CodeUndecodable:
		s.responses.Fail(w, wire.ApiErrorCodeAttachmentUndecodable, "the attachment content could not be read")
	default:
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "the attachment could not be accepted")
	}
}

func (s *Submission) publishAttachmentNotice(ctx context.Context, sessionID string, sess *wire.Session, notice error) {
	if sess == nil || notice == nil {
		return
	}
	hostErr := renderPromptHostError(s.responses.Notices, notice, false)
	if hostErr.Code == "" {
		hostErr.Code = promptHostErrorCode(notice)
	}
	s.EventPublisher.PublishSessionHostError(ctx, sess.ProjectID, sessionID, hostErr)
}

// ErrAttachmentScannedNoText marks attachments without extractable text.
var ErrAttachmentScannedNoText error = noticeerr.NewSentinel("attachment_scanned_no_text", wire.NoticeCodeAttachmentScannedNoText)
