package promptadmin

import (
	"context"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/tooloutput"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	attachmentUploadReadTimeout = 2 * time.Minute
	attachmentReclaimBatchSize  = 128
)

// HandleUploadAttachment streams one body into project host data.
func (s *Handler) HandleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	p, ok := requestscope.ProjectByURLID(s.Projects, s.responses, w, r)
	if !ok {
		return
	}
	projectID := p.ID
	filename := strings.TrimSpace(r.URL.Query().Get("filename"))
	if filename == "" {
		s.responses.InvalidQueryParam(w, "filename", "filename query parameter required")
		return
	}
	reportedMIME := ""
	if raw := strings.TrimSpace(r.URL.Query().Get("mime")); raw != "" {
		parsed, _, err := mime.ParseMediaType(raw)
		if err != nil || !strings.Contains(parsed, "/") {
			s.responses.InvalidQueryParam(w, "mime", "must be a media type such as image/png")
			return
		}
		reportedMIME = parsed
	}
	if err := httpio.RequireRequestMediaType(r, "application/octet-stream"); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	store, ok := s.AttachmentStore(r.Context(), projectID)
	if !ok {
		s.responses.Unavailable(w, wire.ApiErrorCodeAttachmentUnavailable, "attachment storage is not configured for this project")
		return
	}
	caps := s.Caps
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(attachmentUploadReadTimeout)); err == nil {
		defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
	}
	body := http.MaxBytesReader(w, r.Body, caps.Transport.MaxUpload.Int64()+1)
	defer func() { _ = body.Close() }()

	receipt, err := promptattach.Upload(r.Context(), store, caps, s.Video, filename, reportedMIME, body)
	if err != nil {
		s.WriteAttachmentError(w, err)
		return
	}
	if err := s.Store.RecordPromptAttachmentBlob(r.Context(), projectID, receipt.BlobID, receipt.Bytes); err != nil {
		_, _ = store.DiscardStagedBefore(receipt.BlobID, time.Time{})
		s.responses.InternalError(w, r, err)
		return
	}
	if err := s.maintainPromptAttachments(r.Context(), projectID, store); err != nil && s.responses.Logger != nil {
		s.responses.Logger.WarnContext(r.Context(), "maintain prompt attachments", "project_id", projectID, "error", err)
	}
	res := wire.AttachmentUploadResponse{
		BlobID:   receipt.BlobID,
		Filename: receipt.Filename,
		Mime:     receipt.MIME,
		Kind:     wire.AttachmentKind(receipt.Kind),
		Bytes:    receipt.Bytes,
	}
	if v := receipt.Video; v != nil {
		res.Video = &wire.AttachmentVideoFacts{DurationMs: int(math.Round(v.DurationMS)), Width: v.Width, Height: v.Height}
	}
	httpio.WriteJSON(w, http.StatusCreated, res)
}

// attachmentStore resolves one project's attachment store.
func (s *Handler) AttachmentStore(ctx context.Context, projectID string) (blobstore.Store, bool) {
	root := strings.TrimSpace(s.Sessions.Workspace.HostDataDir(projectID))
	if root == "" {
		return blobstore.Store{}, false
	}
	store := blobstore.Store{
		Root: root, Dir: tooloutput.AttachmentSpillDir,
		StagedTTL: promptattach.StagedTTL,
		MaintenanceLease: func() (func(), bool) {
			lifecycle := bloblifecycle.ForDevice(s.DataDir)
			if !lifecycle.TryLock() {
				return nil, false
			}
			return lifecycle.Unlock, true
		},
		Retained: func(blobID string) (bool, error) {
			return s.Store.PromptAttachmentBlobRetained(ctx, projectID, blobID)
		},
		Released: func(blobID string) error {
			return s.Store.DeletePromptAttachmentBlob(ctx, projectID, blobID)
		},
	}
	return store, store.Available()
}

// promptAttachmentMaintenanceInterval paces the sweep of staged attachments
// nobody sent. Uploads also sweep their own project, so the runner only has
// to catch what a session left behind.
const promptAttachmentMaintenanceInterval = time.Hour

// RunPromptAttachmentMaintenance sweeps every project's staged attachments at
// boot and on an hourly cadence.
func (s *Handler) RunPromptAttachmentMaintenance(ctx context.Context) error {
	sweep := func() {
		projects, err := s.Projects.List(ctx)
		if err != nil {
			s.responses.Logger.WarnContext(ctx, "prompt attachment maintenance: list projects", "err", err)
			return
		}
		for _, p := range projects {
			store, ok := s.AttachmentStore(ctx, p.ID)
			if !ok {
				continue
			}
			if err := s.maintainPromptAttachments(ctx, p.ID, store); err != nil {
				s.responses.Logger.WarnContext(ctx, "prompt attachment maintenance", "project_id", p.ID, "err", err)
			}
		}
	}
	sweep()
	ticker := time.NewTicker(promptAttachmentMaintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			sweep()
		}
	}
}

func (s *Handler) maintainPromptAttachments(ctx context.Context, projectID string, store blobstore.Store) error {
	cutoff := time.Now().Add(-promptattach.StagedTTL)
	candidates, err := s.Store.ListPromptAttachmentReclaimCandidates(ctx, projectID, cutoff, attachmentReclaimBatchSize)
	if err != nil {
		return err
	}
	for _, blobID := range candidates {
		if _, err := store.DiscardStagedBefore(blobID, cutoff); err != nil {
			return err
		}
	}
	return store.Maintain()
}
