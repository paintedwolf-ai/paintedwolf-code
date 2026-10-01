package visual

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/storageusage"
	"github.com/lycaon/lycaon/pkg/api"
)

// LiveToolRecordingMeta returns metadata for invocation-scoped preview video.
func LiveToolRecordingMeta(mime, pageID, originMessageID, toolCallID string, recordedAt time.Time, durationMS int64) api.VisualArtifact {
	return api.VisualArtifact{
		Mime: mime, Source: api.VisualArtifactSourceCapture, Caption: "Live tool recording",
		PageID: pageID, OriginMessageID: originMessageID, ToolCallID: toolCallID,
		RecordedAt: &recordedAt, DurationMS: durationMS,
	}
}

// Entry is a stored artifact with bytes for fetch and perceive resolution.
type Entry struct {
	Meta               api.VisualArtifact
	Bytes              []byte
	AutomaticRecording bool
	// ProducerSessionID is the page-holder session.
	ProducerSessionID string
	// WorkflowRunID scopes the artifact to a run.
	WorkflowRunID string
	// OperationID makes one client write idempotent.
	OperationID string
	// NaturalKey identifies a replaceable product slot.
	NaturalKey string
}

// ArtifactReference defines a reference to an artifact.
type ArtifactReference struct {
	Kind       api.ArtifactReferenceKind `json:"kind"`
	MessageID  string                    `json:"message_id,omitempty"`
	SessionID  string                    `json:"session_id,omitempty"`
	ToolCallID string                    `json:"tool_call_id,omitempty"`
	CreatedAt  time.Time                 `json:"created_at"`
}

// ArtifactDeleteResult records the outcome of tombstoning an artifact.
type ArtifactDeleteResult struct {
	ID         string              `json:"id"`
	DeletedAt  time.Time           `json:"deleted_at"`
	References []ArtifactReference `json:"references"`
}

// Store persists artifact bytes within session trees.
type Store interface {
	Put(ctx context.Context, rootSessionID string, entry Entry) (api.VisualArtifact, error)
	// Resolve returns Present or a typed absence.
	Resolve(ctx context.Context, rootSessionID, artifactID string) Resolution
	// ListProject returns one project-scoped metadata page (no bytes), oldest-first.
	ListProject(ctx context.Context, projectID string, query ArtifactPageQuery) (ArtifactPage, error)
	// ListTree returns tree-scoped artifact metadata for the root session (no bytes).
	ListTree(ctx context.Context, rootSessionID string) ([]api.ArtifactListItem, error)
	// FindByEvidenceHandle returns the newest tree-scoped match.
	FindByEvidenceHandle(ctx context.Context, rootSessionID, handle string) (string, error)
	// Delete tombstones an artifact and reports its references.
	Delete(ctx context.Context, projectID, artifactID, reason string) (ArtifactDeleteResult, bool, error)
	// DeleteGroup atomically tombstones a bounded group under the deletion guard.
	DeleteGroup(ctx context.Context, projectID string, artifactIDs []string, reason string) (int64, error)
	// CollectGarbage retries a bounded batch of committed body removals.
	CollectGarbage(ctx context.Context) error
	// Discard removes an artifact that has no durable references.
	Discard(ctx context.Context, projectID, artifactID string) error
	// StorageUsage reports retained artifact bytes.
	StorageUsage(ctx context.Context, projectID string) (storageusage.Usage, error)
}

const (
	DefaultArtifactPageLimit = 100
	MaxArtifactPageLimit     = 500
)

// ArtifactPageQuery is an exclusive stable cursor over artifact creation order.
type ArtifactPageQuery struct {
	AfterCreatedAt string
	AfterID        string
	Limit          int
}

func (q ArtifactPageQuery) effectiveLimit() int {
	if q.Limit <= 0 {
		return DefaultArtifactPageLimit
	}
	if q.Limit > MaxArtifactPageLimit {
		return MaxArtifactPageLimit
	}
	return q.Limit
}

// ArtifactPage is one bounded project listing and its next internal cursor.
type ArtifactPage struct {
	Items         []api.ArtifactListItem
	NextCreatedAt string
	NextID        string
}

// prepareEntry validates and copies one artifact write.
func prepareEntry(entry Entry) (api.VisualArtifact, []byte, error) {
	raw := entry.Bytes
	if len(raw) == 0 {
		return api.VisualArtifact{}, nil, fmt.Errorf("artifact bytes are required")
	}
	mime := strings.TrimSpace(entry.Meta.Mime)
	if mime == "" {
		return api.VisualArtifact{}, nil, fmt.Errorf("artifact mime is required")
	}
	if max := MaxBytesForMime(mime); len(raw) > max {
		return api.VisualArtifact{}, nil, fmt.Errorf("artifact exceeds max bytes (%d)", max)
	}
	raw = append([]byte(nil), raw...)
	width, height, err := rasterSize(mime, raw)
	if err != nil {
		return api.VisualArtifact{}, nil, err
	}
	if IsFrameArchiveMime(mime) {
		// An archive's frames are sized by the viewport its producer declared.
		width, height = max(0, entry.Meta.Width), max(0, entry.Meta.Height)
	}
	id := normalizeID(entry.Meta.ID)
	if id == "" {
		id = uuid.NewString()
	}
	return api.VisualArtifact{
		ID:              id,
		Mime:            mime,
		Source:          entry.Meta.Source,
		Caption:         entry.Meta.Caption,
		EvidenceHandle:  entry.Meta.EvidenceHandle,
		PageID:          entry.Meta.PageID,
		RecordedAt:      entry.Meta.RecordedAt,
		DurationMS:      entry.Meta.DurationMS,
		Width:           width,
		Height:          height,
		Perceive:        entry.Meta.Perceive,
		ToolCallID:      entry.Meta.ToolCallID,
		OriginMessageID: strings.TrimSpace(entry.Meta.OriginMessageID),
	}, raw, nil
}

func normalizeKey(sessionID string) string {
	return strings.TrimSpace(sessionID)
}

func normalizeID(id string) string {
	return strings.TrimSpace(id)
}
