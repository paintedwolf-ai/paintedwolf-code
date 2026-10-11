package editoradmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/api/secretview"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/secretspan"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// editorScreenMemo caches one screen per document and evidence snapshot.
type editorScreenMemo struct {
	mu      sync.Mutex
	entries map[string]editorScreenEntry
}

type editorScreenEntry struct {
	projectID string
	digest    string
	screen    *wire.SecretScreen
	pending   bool
}

func (m *editorScreenMemo) get(documentID, digest string) (*wire.SecretScreen, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	got, ok := m.entries[documentID]
	if !ok || got.digest != digest || got.screen == nil {
		return nil, false
	}
	return got.screen, true
}

func (m *editorScreenMemo) begin(documentID, projectID, digest string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = map[string]editorScreenEntry{}
	}
	if entry, ok := m.entries[documentID]; ok && entry.digest == digest &&
		(entry.pending || entry.screen != nil) {
		return false
	}
	m.entries[documentID] = editorScreenEntry{projectID: projectID, digest: digest, pending: true}
	return true
}

func (m *editorScreenMemo) complete(documentID, digest string, screen *wire.SecretScreen) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[documentID]
	if !ok || entry.digest != digest || !entry.pending {
		return false
	}
	m.entries[documentID] = editorScreenEntry{projectID: entry.projectID, digest: digest, screen: screen}
	return true
}

func (m *editorScreenMemo) invalidate(documentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, documentID)
}

// EditorScreenProjection starts screening without delaying document delivery.
func (s *Handler) EditorScreenProjection(ctx context.Context, d *editordoc.Document) (wire.SecretScreenStatus, *wire.SecretScreen) {
	if s == nil || d == nil || s.SecretSpans == nil || !s.SecretSpans.Ready() {
		return wire.SecretScreenUnavailable, nil
	}
	ctx, evidence, err := secretview.ProjectContext(s.ManagedSecrets, ctx, d.ProjectID)
	if err != nil {
		return wire.SecretScreenUnavailable, nil
	}
	sum := sha256.Sum256([]byte(d.Draft))
	digest := hex.EncodeToString(sum[:]) + evidence + s.SecretSpans.ClassificationRevision(ctx)
	if cached, ok := s.editorScreens.get(d.ID, digest); ok {
		projection := *cached
		projection.ScreenedRevision = d.Revision
		return wire.SecretScreenComplete, &projection
	}
	if s.editorScreens.begin(d.ID, d.ProjectID, digest) {
		document := *d
		s.background.Go(ctx, func(ctx context.Context) {
			s.finishEditorScreen(ctx, document, digest)
		})
	}
	return wire.SecretScreenPending, nil
}

func (s *Handler) finishEditorScreen(ctx context.Context, d editordoc.Document, digest string) {
	screen := secretview.ScreenText(s.SecretSpans, ctx, d.Draft)
	if screen == nil {
		s.editorScreens.invalidate(d.ID)
		return
	}
	screen.ScreenedRevision = d.Revision
	if !s.editorScreens.complete(d.ID, digest, screen) {
		return
	}
	current, err := s.EditorDocuments.CurrentSnapshot(ctx, d.ProjectID, d.ID)
	if err != nil {
		return
	}
	sum := sha256.Sum256([]byte(current.Draft))
	_, evidence, err := secretview.ProjectContext(s.ManagedSecrets, ctx, current.ProjectID)
	if err != nil || hex.EncodeToString(sum[:])+evidence+s.SecretSpans.ClassificationRevision(ctx) != digest || s.Events == nil {
		return
	}
	_ = s.Events.Publish(ctx, wire.EventTopicEditorDocument,
		events.PublishKey{Project: current.ProjectID, Facet: current.ID},
		s.EditorDocumentEventDTO(ctx, current, false))
}

// editorSecretTarget resolves a range against its selected revision.
func (s *Handler) editorSecretTarget(
	w http.ResponseWriter, r *http.Request, rng wire.SecretMarkRange,
) (*project.Project, *editordoc.Document, bool) {
	p, ok := s.editorProject(w, r)
	if !ok {
		return nil, nil, false
	}
	d, err := s.EditorDocuments.Snapshot(r.Context(), p.ID, strings.TrimSpace(chi.URLParam(r, "document_id")), rng.Revision)
	if err != nil {
		s.writeEditorDocumentError(w, r, err)
		return nil, nil, false
	}
	return p, d, true
}

// capture resolves offsets to host-held bytes.
func (s *Handler) capture(
	w http.ResponseWriter, d *editordoc.Document, rng wire.SecretMarkRange,
) (secretspan.Candidate, string, bool) {
	candidate := secretspan.Propose(d.Draft, rng.Start, rng.End, trimRequested(rng))
	if !candidate.Eligible {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, markRefusal(candidate.Reason))
		return candidate, "", false
	}
	value, ok := secretspan.Slice(d.Draft, candidate.Start, candidate.End)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, markRefusal(secretspan.IneligibleOutOfRange))
		return candidate, "", false
	}
	return candidate, value, true
}

// previewCandidate predicts the mark's refusals, including already-protected bytes.
func (s *Handler) previewCandidate(projectID string, d *editordoc.Document, rng wire.SecretMarkRange) secretspan.Candidate {
	candidate := secretspan.Propose(d.Draft, rng.Start, rng.End, trimRequested(rng))
	if !candidate.Eligible {
		return candidate
	}
	if value, ok := secretspan.Slice(d.Draft, candidate.Start, candidate.End); ok && s.ManagedSecrets.Protects(projectID, value) {
		candidate.Eligible, candidate.Reason = false, secretspan.IneligibleAlreadyProtected
	}
	return candidate
}

// trimRequested treats omitted trim as enabled.
func trimRequested(rng wire.SecretMarkRange) bool {
	return rng.Trim == nil || *rng.Trim
}

func markRefusal(reason secretspan.Ineligible) string {
	switch reason {
	case secretspan.IneligibleTooLarge:
		return "that selection is larger than a credential can be"
	case secretspan.IneligibleTooShort:
		return secretcap.ErrValueTooShort.Error()
	case secretspan.IneligibleEmpty:
		return "that selection is only quotes and whitespace"
	case secretspan.IneligibleAlreadyProtected:
		return "that selection is already protected by a managed secret"
	default:
		return "the selected range is not in this document"
	}
}

func (s *Handler) HandlePreviewEditorSecretMark(w http.ResponseWriter, r *http.Request) {
	var rng wire.SecretMarkRange
	if err := httpio.DecodeJSON(w, r, &rng); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	p, d, ok := s.editorSecretTarget(w, r, rng)
	if !ok {
		return
	}
	c := s.previewCandidate(p.ID, d, rng)
	httpio.WriteJSON(w, http.StatusOK, wire.SecretMarkPreview{
		Eligible: c.Eligible, Reason: string(c.Reason), Start: c.Start, End: c.End,
		RuneLength: c.RuneLength, ByteLength: c.ByteLength, Shape: c.Shape,
		TrimmedLeading: c.TrimmedLeading, TrimmedTrailing: c.TrimmedTrailing,
	})
}

func (s *Handler) HandleMarkEditorSecret(w http.ResponseWriter, r *http.Request) {
	var body wire.SecretMarkRequest
	if err := httpio.DecodeJSON(w, r, &body); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	p, d, ok := s.editorSecretTarget(w, r, body.Range)
	if !ok {
		return
	}
	_, value, ok := s.capture(w, d, body.Range)
	if !ok {
		return
	}
	operationID := strings.TrimSpace(body.OperationID)
	if operationID == "" {
		operationID = uuid.NewString()
	}
	put, err := s.ManagedSecrets.Put(r.Context(), secretcap.PutRequest{
		ProjectID: p.ID, OperationID: operationID, PersonID: requestscope.Caller(r).ID,
		Name: strings.TrimSpace(body.Name), Purpose: strings.TrimSpace(body.Purpose),
		Scope: secretcap.ScopeProject, Origin: secretcap.OriginFileMarked, Value: value,
	})
	if err != nil {
		secretview.WriteError(s.responses, w, r, err)
		return
	}
	personactions.Note(r.Context(), "secret_reference", put.Metadata.Reference)
	s.republishEditorDocument(r.Context(), d)
	httpio.WriteJSON(w, http.StatusOK, secretview.Metadata(put.Metadata))
}

// republishEditorDocument invalidates cached spans before publishing.
func (s *Handler) republishEditorDocument(ctx context.Context, d *editordoc.Document) {
	if s == nil || d == nil {
		return
	}
	s.editorScreens.invalidate(d.ID)
	if s.Events == nil {
		return
	}
	_ = s.Events.Publish(ctx, wire.EventTopicEditorDocument,
		events.PublishKey{Project: d.ProjectID, Facet: d.ID}, s.EditorDocumentEventDTO(ctx, d, false))
}

// documents returns only the identities of screens already requested by a viewer.
func (m *editorScreenMemo) documents(projectID string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	documents := make(map[string]string)
	for id, entry := range m.entries {
		if projectID == "" || entry.projectID == projectID {
			documents[id] = entry.projectID
		}
	}
	return documents
}

func (s *Handler) RefreshProjectSecretScreens(ctx context.Context, projectID string) {
	documents := s.editorScreens.documents(projectID)
	if len(documents) == 0 {
		return
	}
	s.background.Go(ctx, func(ctx context.Context) {
		for id, project := range documents {
			document, err := s.EditorDocuments.CurrentSnapshot(ctx, project, id)
			if err == nil {
				s.republishEditorDocument(ctx, document)
			}
		}
	})
}
