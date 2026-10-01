package promptadmin

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/noticeerr"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// ingestPromptImages stores attached rasters in the visual plane.
func (s *Handler) ingestPromptImages(ctx context.Context, sessionID, operationID, group string, images []promptattach.InlineImage) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
	}
	for i, img := range images {
		if int64(len(img.Bytes)) > s.Caps.Transport.MaxImage.Int64() {
			return nil, &attacherr.Error{
				Code:    attacherr.CodeTooLarge,
				Message: fmt.Sprintf("image[%d]: exceeds per-file byte cap", i),
			}
		}
		if err := visual.RejectUnsafeUserImage(img.Mime, img.Bytes); err != nil {
			return nil, &attacherr.Error{
				Code:    attacherr.CodeUnsupported,
				Message: fmt.Sprintf("image[%d]: %v", i, err),
			}
		}
	}
	root := session.RootSessionID(ctx, s.Store, sessionID)
	ids := make([]string, 0, len(images))
	for i, img := range images {
		artifactID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":"+group+":"+fmt.Sprint(i))).String()
		wireArt, err := visual.IngestUserImage(ctx, s.VisualStore, root, artifactID, img.Mime, img.Bytes)
		if err != nil {
			return append(ids, artifactID), &attacherr.Error{
				Code:    attacherr.CodeUnsupported,
				Message: fmt.Sprintf("image[%d]: %v", i, err),
			}
		}
		ids = append(ids, wireArt.ID)
	}
	return ids, nil
}

func (s *Handler) discardPromptImages(ctx context.Context, projectID string, artifactIDs []string) {
	for _, artifactID := range artifactIDs {
		if err := s.VisualStore.Discard(ctx, projectID, artifactID); err != nil && s.responses.Logger != nil {
			s.responses.Logger.WarnContext(ctx, "discard unadmitted prompt image", "artifact_id", artifactID, "error", err)
		}
	}
}

func (s *Handler) publishUserImageNotVisible(ctx context.Context, sessionID string, sess *wire.Session) {
	if sess == nil {
		return
	}
	hostErr := renderPromptHostError(s.responses.Notices, ErrUserImageNotVisible, false)
	if hostErr.Code == "" {
		hostErr.Code = "user_image_not_visible"
	}
	s.EventPublisher.PublishSessionHostError(ctx, sess.ProjectID, sessionID, hostErr)
}

// ErrUserImageNotVisible marks images hidden from the selected model.
var ErrUserImageNotVisible error = noticeerr.NewSentinel("user_image_not_visible", wire.NoticeCodeUserImageNotVisible)
