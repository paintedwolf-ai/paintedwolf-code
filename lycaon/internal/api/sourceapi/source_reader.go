package sourceapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcecomparison"
	"github.com/lycaon/lycaon/internal/sourceledger"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func readerEndpoint(side wire.SourceComparisonSide) *wire.SourceReaderEndpoint {
	var screen *wire.SecretScreen
	if side.SecretScreen != nil {
		value := *side.SecretScreen
		value.Spans = nil
		screen = &value
	}
	return &wire.SourceReaderEndpoint{VersionID: side.VersionID, RootID: side.RootID, Path: side.Path, State: side.State, Sha256: side.Sha256, SizeBytes: side.SizeBytes, Availability: side.Availability, Reason: side.Reason, SecretScreen: screen}
}

func readerTextSide(path, text string) wire.SourceComparisonSide {
	return wire.SourceComparisonSide{Path: path, Content: text, State: "content", Availability: "available", Sha256: sourcecomparison.Hash(text), SizeBytes: int64(len(text))}
}

func (s *Handler) readChatFileEdit(ctx context.Context, sessionID string, ref wire.FileEditReference) (wire.FileEditSnapshot, error) {
	msg, err := s.SessionStore.GetMessage(ctx, sessionID, ref.MessageID)
	if err != nil {
		return wire.FileEditSnapshot{}, err
	}
	msg = messageview.RedactMessage(msg)
	if result := msg.ToolResult; result != nil && result.ToolCallID == ref.ToolCallID {
		if ref.Index == 0 && result.FileEdit != nil {
			return *result.FileEdit, nil
		}
		if ref.Index > 0 && result.OverlayPromotion != nil && ref.Index <= len(result.OverlayPromotion.Files) {
			return result.OverlayPromotion.Files[ref.Index-1], nil
		}
	}
	return wire.FileEditSnapshot{}, errors.New("file edit not found")
}

func (s *Handler) readerCurrentSide(ctx context.Context, p *project.Project, rootID, path string) (wire.SourceComparisonSide, error) {
	snapshot, err := s.EditorDocuments.ResolveSourceSnapshot(ctx, p, project.SourceReadRequest{RootID: rootID, Path: path}, editordoc.ObserveCurrent)
	if errors.Is(err, project.ErrSourceNotFound) {
		return wire.SourceComparisonSide{Path: path, State: "absent", Availability: "absent"}, nil
	}
	if err != nil {
		return wire.SourceComparisonSide{}, err
	}
	if snapshot.Document != nil {
		return readerTextSide(path, snapshot.Document.Draft), nil
	}
	read := snapshot.Source
	if read.Binary || read.OverLimit {
		return wire.SourceComparisonSide{Path: path, State: "content", Availability: "unavailable"}, nil
	}
	return readerTextSide(path, read.Content), nil
}

func (s *Handler) writeSourceComparison(w http.ResponseWriter, r *http.Request, projectID string, comparison sourceledger.Comparison) {
	httpio.WriteJSON(w, http.StatusOK, s.MapSourceComparison(r.Context(), projectID, comparison))
}
