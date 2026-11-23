package secretcap

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// repository coordinates encrypted values and their durable metadata.
type repository struct {
	durable                durableEvidence
	mutationMu             sync.Mutex
	screeningGeneration    atomic.Uint64
	handle                 db.Handle
	queries                *db.Queries
	values                 vaultValues
	remember               RememberFunc
	now                    func() time.Time
	onScreeningInvalidated []func(context.Context, string)
}

type facts struct {
	// chatPresent and chatTitle describe the owning chat when it still exists.
	chatPresent     bool
	chatTitle       string
	version         int64
	valueID         string
	valueReplacedAt string
	useCount        int64
	lastUsedAt      string
	revealCount     int64
	lastRevealedAt  string
}

func (s *repository) invalidateScreening(ctx context.Context, projectID string) {
	for _, observer := range s.onScreeningInvalidated {
		observer(ctx, projectID)
	}
}

func (s *repository) metadataList(ctx context.Context, projectID string, rows []db.ManagedSecrets) ([]Metadata, error) {
	byID, err := s.projectFacts(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]Metadata, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.metadata(row, byID[row.ID]))
	}
	return out, nil
}

func (s *repository) metadataRow(ctx context.Context, row db.ManagedSecrets) (Metadata, error) {
	f, err := s.secretFacts(ctx, row.ID)
	if err != nil {
		return Metadata{}, err
	}
	return s.metadata(row, f), nil
}

// projectFacts loads current versions and use summaries for one project.
func (s *repository) projectFacts(ctx context.Context, projectID string) (map[string]facts, error) {
	versions, err := s.queries.ListProjectManagedSecretVersions(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]facts, len(versions))
	for _, version := range versions {
		if version.RetiredAt.Valid {
			continue
		}
		entry := out[version.SecretID]
		entry.version, entry.valueID = version.Version, version.ID
		if version.Version > 1 {
			entry.valueReplacedAt = version.CreatedAt
		}
		out[version.SecretID] = entry
	}
	usage, err := s.queries.ListProjectManagedSecretUsage(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, summary := range usage {
		entry := out[summary.SecretID]
		entry.useCount, entry.lastUsedAt = summary.UseCount, summary.LastUsedAt
		out[summary.SecretID] = entry
	}
	reveals, err := s.queries.ListProjectManagedSecretRevealSummaries(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, summary := range reveals {
		entry := out[summary.SecretID]
		entry.revealCount, entry.lastRevealedAt = summary.RevealCount, summary.LastRevealedAt
		out[summary.SecretID] = entry
	}
	chats, err := s.queries.ListProjectManagedSecretChats(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, chat := range chats {
		entry := out[chat.SecretID]
		entry.chatPresent, entry.chatTitle = true, chat.Title.String
		out[chat.SecretID] = entry
	}
	return out, nil
}

func (s *repository) secretFacts(ctx context.Context, secretID string) (facts, error) {
	var out facts
	current, hasCurrent, err := s.currentVersion(ctx, secretID)
	if err != nil {
		return facts{}, err
	}
	if hasCurrent {
		out.version, out.valueID = current.Version, current.ID
		if current.Version > 1 {
			out.valueReplacedAt = current.CreatedAt
		}
	}
	usage, err := s.queries.GetManagedSecretUsage(ctx, secretID)
	if err != nil {
		return facts{}, err
	}
	out.useCount, out.lastUsedAt = usage.UseCount, usage.LastUsedAt
	reveals, err := s.queries.GetManagedSecretRevealSummary(ctx, secretID)
	if err != nil {
		return facts{}, err
	}
	out.revealCount, out.lastRevealedAt = reveals.RevealCount, reveals.LastRevealedAt
	title, err := s.queries.GetManagedSecretChat(ctx, secretID)
	switch {
	case err == nil:
		out.chatPresent, out.chatTitle = true, title.String
	case errors.Is(err, sql.ErrNoRows):
	default:
		return facts{}, err
	}
	return out, nil
}

func (s *repository) metadata(row db.ManagedSecrets, f facts) Metadata {
	meta := Metadata{
		Reference: secretmatch.ReferenceToken(row.ID), Name: row.Name, Purpose: row.Purpose,
		Scope: row.Scope, Origin: row.Origin, CreatedAt: row.CreatedAt,
		State: s.state(row, f), Version: f.version, UseCount: f.useCount,
		RevealCount: f.revealCount,
	}
	if entry, ok := s.values.get(f.valueID); ok && f.valueID != "" {
		meta.Custody = entry.Custody
	}
	if row.ChatSessionID.Valid {
		value := row.ChatSessionID.String
		meta.ChatSessionID = &value
		meta.ChatTitle, meta.ChatDeleted = f.chatTitle, !f.chatPresent
	}
	if row.Format.Valid {
		meta.Format = row.Format.String
	}
	if row.EntropyBits.Valid {
		meta.EntropyBits = row.EntropyBits.Int64
	}
	if row.AgentUseEndsAt.Valid {
		value := row.AgentUseEndsAt.String
		meta.AgentUseEndsAt = &value
	}
	if f.valueReplacedAt != "" {
		value := f.valueReplacedAt
		meta.ValueReplacedAt = &value
	}
	if f.lastUsedAt != "" {
		value := f.lastUsedAt
		meta.LastUsedAt = &value
	}
	if f.lastRevealedAt != "" {
		value := f.lastRevealedAt
		meta.LastRevealedAt = &value
	}
	return meta
}

// state prioritizes terminal, missing-value, then agent-use constraints.
func (s *repository) state(row db.ManagedSecrets, f facts) string {
	if row.RevokedAt.Valid {
		return StateRevoked
	}
	if f.valueID == "" {
		return StateUnavailable
	}
	if _, ok := s.values.get(f.valueID); !ok {
		return StateUnavailable
	}
	if agentUseEnded(row.AgentUseEndsAt, s.now()) {
		return StateAgentUseExpired
	}
	return StateActive
}

func (s *repository) rowForReference(ctx context.Context, reference string) (db.ManagedSecrets, error) {
	id, err := ParseReference(reference)
	if err != nil {
		return db.ManagedSecrets{}, err
	}
	row, err := s.queries.GetManagedSecret(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.ManagedSecrets{}, ErrNotFound
	}
	return row, err
}

// projectRow accepts every capability scope in the project.
func (s *repository) projectRow(ctx context.Context, projectID, reference string) (db.ManagedSecrets, error) {
	row, err := s.rowForReference(ctx, reference)
	if err != nil {
		return db.ManagedSecrets{}, err
	}
	if row.ProjectID != strings.TrimSpace(projectID) {
		return db.ManagedSecrets{}, ErrNotVisible
	}
	return row, nil
}
