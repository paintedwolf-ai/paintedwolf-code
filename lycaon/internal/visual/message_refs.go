package visual

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// MessageRefs derives every artifact claim made by one transcript row.
func MessageRefs(projectID, sessionID string, msg api.Message) []RefWrite {
	projectID = strings.TrimSpace(projectID)
	messageID := strings.TrimSpace(msg.ID)
	if projectID == "" || messageID == "" {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	base := RefWrite{ProjectID: projectID, MessageID: messageID, SessionID: sessionID}

	listKind := api.ArtifactReferenceKindMessagePresent
	if msg.Role == api.MessageRoleUser {
		listKind = api.ArtifactReferenceKindMessageAttachment
	}

	var out []RefWrite
	add := func(kind api.ArtifactReferenceKind, id, toolCallID string) {
		if id = strings.TrimSpace(id); id == "" {
			return
		}
		ref := base
		ref.Kind = kind
		ref.ArtifactID = id
		ref.ToolCallID = strings.TrimSpace(toolCallID)
		out = append(out, ref)
	}

	for _, id := range msg.ArtifactIDs {
		add(listKind, id, "")
	}
	if tr := msg.ToolResult; tr != nil && tr.Visual != nil {
		add(api.ArtifactReferenceKindToolResult, tr.Visual.ID, tr.ToolCallID)
	}
	if fb := msg.WorkflowFeedback; fb != nil {
		add(api.ArtifactReferenceKindMessageAttachment, fb.ArtifactID, "")
		for _, id := range fb.ArtifactIDs {
			add(api.ArtifactReferenceKindMessageAttachment, id, "")
		}
	}
	return out
}

func artifactIDs(records []ArtifactRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

// ReferenceCountsFor returns claim counts only for the requested page.
func (c recordsCatalog) ReferenceCountsFor(ctx context.Context, projectID string, records []ArtifactRecord) (map[string][]api.ArtifactReferenceCount, error) {
	r := c.r
	out := make(map[string][]api.ArtifactReferenceCount, len(records))
	if len(records) == 0 {
		return out, nil
	}
	rows, err := r.queries.ListProjectArtifactRefCountsFor(ctx, db.ListProjectArtifactRefCountsForParams{
		ProjectID: strings.TrimSpace(projectID), ArtifactIds: artifactIDs(records),
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ArtifactID] = append(out[row.ArtifactID], api.ArtifactReferenceCount{
			Kind: api.ArtifactReferenceKind(row.Kind), Count: int(row.RefCount),
		})
	}
	return out, nil
}

// ProjectOriginsFor returns first transcript origins only for the requested page.
func (c recordsCatalog) ProjectOriginsFor(ctx context.Context, projectID string, records []ArtifactRecord) (map[string]ArtifactOrigin, error) {
	r := c.r
	out := make(map[string]ArtifactOrigin, len(records))
	if len(records) == 0 {
		return out, nil
	}
	rows, err := r.queries.ListProjectArtifactOriginsFor(ctx, db.ListProjectArtifactOriginsForParams{
		ProjectID: strings.TrimSpace(projectID), ArtifactIds: artifactIDs(records),
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, exists := out[row.ArtifactID]; !exists {
			out[row.ArtifactID] = ArtifactOrigin{MessageID: row.MessageID.String, ToolCallID: row.ToolCallID}
		}
	}
	return out, nil
}

// References lists every durable claim on an artifact.
func (c recordsCatalog) References(ctx context.Context, artifactID string) ([]ArtifactReference, error) {
	r := c.r
	if !r.ready() {
		return nil, nil
	}
	rows, err := r.queries.ListArtifactRefs(ctx, strings.TrimSpace(artifactID))
	if err != nil {
		return nil, err
	}
	out := make([]ArtifactReference, 0, len(rows))
	for _, row := range rows {
		ref := ArtifactReference{
			Kind:       api.ArtifactReferenceKind(row.Kind),
			MessageID:  row.MessageID.String,
			SessionID:  row.SessionID.String,
			ToolCallID: row.ToolCallID,
		}
		if ts, perr := time.Parse(time.RFC3339Nano, row.CreatedAt); perr == nil {
			ref.CreatedAt = ts
		}
		out = append(out, ref)
	}
	return out, nil
}

// ArtifactOrigin is the transcript row that first claimed an artifact. Both
// halves travel together: a tool call names a tool row, a message id names
// every other row.
type ArtifactOrigin struct {
	MessageID  string
	ToolCallID string
}

// TreeOrigins maps artifact id to first referring row within one tree.
func (c recordsCatalog) TreeOrigins(ctx context.Context, rootSessionID string) (map[string]ArtifactOrigin, error) {
	r := c.r
	if !r.ready() {
		return nil, nil
	}
	rows, err := r.queries.ListTreeArtifactOrigins(ctx, nullString(rootSessionID))
	if err != nil {
		return nil, err
	}
	out := make(map[string]ArtifactOrigin, len(rows))
	for _, row := range rows {
		if _, seen := out[row.ArtifactID]; !seen {
			out[row.ArtifactID] = ArtifactOrigin{
				MessageID:  row.MessageID.String,
				ToolCallID: row.ToolCallID,
			}
		}
	}
	return out, nil
}

// RefRowID is the deterministic key for one claim.
func RefRowID(kind api.ArtifactReferenceKind, artifactID, messageID, sessionID, toolCallID string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(kind),
		strings.TrimSpace(artifactID),
		strings.TrimSpace(messageID),
		strings.TrimSpace(sessionID),
		strings.TrimSpace(toolCallID),
	}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// RefWrite is one claim to record inside a referrer's transaction.
type RefWrite struct {
	Kind       api.ArtifactReferenceKind
	ArtifactID string
	ProjectID  string
	MessageID  string
	SessionID  string
	ToolCallID string
}

// WriteRefsTx records valid project claims in the caller's transaction.
func WriteRefsTx(ctx context.Context, qtx *db.Queries, refs []RefWrite) error {
	if qtx == nil || len(refs) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, ref := range refs {
		artifactID := strings.TrimSpace(ref.ArtifactID)
		projectID := strings.TrimSpace(ref.ProjectID)
		if artifactID == "" || projectID == "" || ref.Kind == "" {
			continue
		}
		if err := qtx.UpsertArtifactRef(ctx, db.UpsertArtifactRefParams{
			ID:         RefRowID(ref.Kind, artifactID, ref.MessageID, ref.SessionID, ref.ToolCallID),
			Kind:       string(ref.Kind),
			MessageID:  nullString(ref.MessageID),
			SessionID:  nullString(ref.SessionID),
			ToolCallID: strings.TrimSpace(ref.ToolCallID),
			CreatedAt:  now,
			ID_2:       artifactID,
			ProjectID:  projectID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ClearMessageRefsTx resets a message's claims.
func ClearMessageRefsTx(ctx context.Context, qtx *db.Queries, messageID string) error {
	if qtx == nil || strings.TrimSpace(messageID) == "" {
		return nil
	}
	return qtx.DeleteMessageArtifactRefs(ctx, nullString(messageID))
}

// ClearProjectRefsOfKindTx releases one project claim kind.
func ClearProjectRefsOfKindTx(ctx context.Context, qtx *db.Queries, projectID string, kind api.ArtifactReferenceKind) error {
	if qtx == nil || strings.TrimSpace(projectID) == "" {
		return nil
	}
	return qtx.DeleteProjectArtifactRefsOfKind(ctx, db.DeleteProjectArtifactRefsOfKindParams{
		ProjectID: strings.TrimSpace(projectID),
		Kind:      string(kind),
	})
}

