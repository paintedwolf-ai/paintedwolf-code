// Package secretcap manages protected secret capabilities.
package secretcap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

const (
	ScopeChat    = "chat"
	ScopeProject = "project"

	OriginGenerated       = "generated"
	OriginAskUserResponse = "ask_user_response"
	OriginDetected        = "detected"
	OriginFileMarked      = "file_marked"
	OriginComposerMarked  = "composer_marked"
	OriginSettingsEntered = "settings_entered"
	// OriginCookieJar stores a serialized jar; each cookie value is screened.
	OriginCookieJar = "cookie_jar"
	// OriginTokenJar stores serialized tokens; each token value is screened.
	OriginTokenJar = "token_jar"

	FormatBase64URL    = "base64url"
	FormatHex          = "hex"
	FormatAlphanumeric = "alphanumeric"

	StateActive          = "active"
	StateRevoked         = "revoked"
	StateAgentUseExpired = "agent_use_expired"
	StateUnavailable     = "unavailable"

	credentialContext    = "managed secret"
	maxReferencesPerCall = 32
	maintenanceInterval  = 5 * time.Minute
	maxAgentUseLifetime  = 365 * 24 * time.Hour
)

var (
	ErrNotFound          = errors.New("secret reference not found")
	ErrNotVisible        = errors.New("secret reference is outside this chat or project")
	ErrRevoked           = errors.New("secret reference is revoked")
	ErrAgentUseExpired   = errors.New("secret reference has passed its agent-use deadline")
	ErrValueMissing      = errors.New("secret value is unavailable")
	ErrInvalidReference  = errors.New("invalid secret reference")
	ErrTooManyReferences = errors.New("too many secret references")
	ErrInvalidGenerate   = errors.New("invalid secret generation request")
	ErrInvalidPut        = errors.New("invalid managed secret")
	ErrInvalidUpdate     = errors.New("invalid managed secret update")
	// ErrValueTooShort rejects values below the exact-match screening floor.
	ErrValueTooShort = fmt.Errorf(
		"a value under %d characters occurs too often in ordinary text for the host to tell it apart",
		secretmatch.MinManagedSecretRunes)
	// ErrAlreadyProtected refuses a file mark over bytes a managed secret
	// already protects: a second capability would duplicate that secret.
	ErrAlreadyProtected = fmt.Errorf("%w: that selection is already protected by a managed secret", ErrInvalidPut)
	// ErrHumanAuthored refuses agent revocation of a person's own capability.
	ErrHumanAuthored = errors.New("secret capability was authored by a person")
	// ErrValueChanged: the value an attestation named was replaced before the
	// attestation completed.
	ErrValueChanged = errors.New("managed secret changed during presence verification")
)

// RememberFunc admits a managed value to exact-match secret screening.
type RememberFunc func(chatSessionID string, values []secretmatch.Remembered)

// Service manages capability metadata and protected values.
type Service struct {
	durable                durableEvidence
	mutationMu             sync.Mutex
	screeningGeneration    atomic.Uint64
	handle                 db.Handle
	queries                *db.Queries
	values                 vaultValues
	remember               RememberFunc
	now                    func() time.Time
	presence               *presence.Broker
	fingerprint            func(string) secretmatch.SecretFingerprint
	onScreeningInvalidated []func(context.Context, string)
}

// AddScreeningInvalidationObserver installs a boot-time observer for protected-value mutations.
func (s *Service) AddScreeningInvalidationObserver(fn func(context.Context, string)) {
	s.onScreeningInvalidated = append(s.onScreeningInvalidated, fn)
}

func (s *Service) invalidateScreening(ctx context.Context, projectID string) {
	for _, observer := range s.onScreeningInvalidated {
		observer(ctx, projectID)
	}
}

// DefaultSlot describes the protected value namespace.
func DefaultSlot() (credentialstore.Slot, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return credentialstore.Slot{}, err
	}
	return credentialstore.Slot{
		Path: credentialstore.DefaultVaultPath(dir), Namespace: credentialstore.NamespaceManagedSecrets,
		Context: credentialContext,
	}, nil
}

// New opens the encrypted credential vault over a durable metadata handle.
func New(database db.Handle, remember RememberFunc) (*Service, error) {
	slot, err := DefaultSlot()
	if err != nil {
		return nil, err
	}
	values, err := credentialstore.Open(slot, validID)
	if err != nil {
		return nil, err
	}
	return NewWithStore(database, values, remember), nil
}

// NewWithStore constructs a service over an explicit value store.
func NewWithStore(database db.Handle, values *credentialstore.Store, remember RememberFunc) *Service {
	return &Service{
		handle: database, queries: db.New(database), values: vaultValues{store: values}, remember: remember,
		now: time.Now,
	}
}

// SetPresence installs the broker that verifies reveals.
func (s *Service) SetPresence(broker *presence.Broker) { s.presence = broker }

// SetFingerprinter installs the screen's identity for exact bytes, so a
// resolution can name which screened values a person holds.
func (s *Service) SetFingerprinter(fp *secretmatch.Fingerprinter) { s.fingerprint = fp.Fingerprint }

// ScreeningGeneration changes when screening gains protected bytes.
func (s *Service) ScreeningGeneration() uint64 {
	if s == nil {
		return 0
	}
	return s.screeningGeneration.Load()
}

func validID(id string) bool {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	return err == nil && parsed.String() == strings.TrimSpace(id)
}

// Metadata is a capability without its protected value.
type Metadata struct {
	Reference     string  `json:"reference"`
	ChatSessionID *string `json:"chat_session_id,omitempty"`
	// ChatTitle names the owning chat while it exists.
	ChatTitle string `json:"chat_title,omitempty"`
	// ChatDeleted marks a chat-scoped capability whose chat was deleted. It
	// stays active for people; no agent can spend it.
	ChatDeleted bool   `json:"chat_deleted,omitempty"`
	Name        string `json:"name"`
	Purpose     string `json:"purpose,omitempty"`
	Scope       string `json:"scope"`
	Origin      string `json:"origin"`
	// Custody comes from the vault entry; empty when no value is available.
	Custody        Custody `json:"custody,omitempty"`
	Format         string  `json:"format,omitempty"`
	EntropyBits    int64   `json:"entropy_bits,omitempty"`
	CreatedAt      string  `json:"created_at"`
	AgentUseEndsAt *string `json:"agent_use_ends_at,omitempty"`
	State          string  `json:"state"`
	// Version counts stored values. Zero means no value is available.
	Version         int64   `json:"version"`
	ValueReplacedAt *string `json:"value_replaced_at,omitempty"`
	// LastUsedAt and UseCount include refused substitutions.
	LastUsedAt     *string `json:"last_used_at,omitempty"`
	UseCount       int64   `json:"use_count"`
	LastRevealedAt *string `json:"last_revealed_at,omitempty"`
	RevealCount    int64   `json:"reveal_count"`
	// LastReleasedAt and ReleaseCount count presence-verified releases.
	LastReleasedAt *string `json:"last_released_at,omitempty"`
	ReleaseCount   int64   `json:"release_count"`
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
	releaseCount    int64
	lastReleasedAt  string
}

// List returns value-free capabilities visible from a chat.
func (s *Service) List(ctx context.Context, projectID, chatSessionID string) ([]Metadata, error) {
	projectID = strings.TrimSpace(projectID)
	rows, err := s.queries.ListVisibleManagedSecrets(ctx, db.ListVisibleManagedSecretsParams{
		ProjectID: projectID, ChatSessionID: nullable(strings.TrimSpace(chatSessionID)),
	})
	if err != nil {
		return nil, err
	}
	return s.metadataList(ctx, projectID, rows)
}

// ListProject returns project metadata without protected values.
func (s *Service) ListProject(ctx context.Context, projectID string) ([]Metadata, error) {
	projectID = strings.TrimSpace(projectID)
	rows, err := s.queries.ListProjectManagedSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.metadataList(ctx, projectID, rows)
}

// Describe returns value-free metadata visible to a chat.
func (s *Service) Describe(ctx context.Context, projectID, chatSessionID, reference string) (Metadata, error) {
	row, err := s.rowForReference(ctx, reference)
	if err != nil {
		return Metadata{}, err
	}
	if err := visible(row, projectID, chatSessionID); err != nil {
		return Metadata{}, err
	}
	return s.metadataRow(ctx, row)
}

func (s *Service) metadataList(ctx context.Context, projectID string, rows []db.ManagedSecrets) ([]Metadata, error) {
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

func (s *Service) metadataRow(ctx context.Context, row db.ManagedSecrets) (Metadata, error) {
	f, err := s.secretFacts(ctx, row.ID)
	if err != nil {
		return Metadata{}, err
	}
	return s.metadata(row, f), nil
}

// projectFacts loads current versions and use summaries for one project.
func (s *Service) projectFacts(ctx context.Context, projectID string) (map[string]facts, error) {
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
	attestations, err := s.queries.ListProjectManagedSecretAttestationSummaries(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, summary := range attestations {
		entry := out[summary.SecretID]
		entry.revealCount, entry.lastRevealedAt = summary.RevealCount, summary.LastRevealedAt
		entry.releaseCount, entry.lastReleasedAt = summary.ReleaseCount, summary.LastReleasedAt
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

func (s *Service) secretFacts(ctx context.Context, secretID string) (facts, error) {
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
	attestations, err := s.queries.GetManagedSecretAttestationSummary(ctx, secretID)
	if err != nil {
		return facts{}, err
	}
	out.revealCount, out.lastRevealedAt = attestations.RevealCount, attestations.LastRevealedAt
	out.releaseCount, out.lastReleasedAt = attestations.ReleaseCount, attestations.LastReleasedAt
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

func (s *Service) metadata(row db.ManagedSecrets, f facts) Metadata {
	meta := Metadata{
		Reference: secretmatch.ReferenceToken(row.ID), Name: row.Name, Purpose: row.Purpose,
		Scope: row.Scope, Origin: row.Origin, CreatedAt: row.CreatedAt,
		State: s.state(row, f), Version: f.version, UseCount: f.useCount,
		RevealCount: f.revealCount, ReleaseCount: f.releaseCount,
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
	if f.lastReleasedAt != "" {
		value := f.lastReleasedAt
		meta.LastReleasedAt = &value
	}
	return meta
}

// state prioritizes terminal, missing-value, then agent-use constraints.
func (s *Service) state(row db.ManagedSecrets, f facts) string {
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

func (s *Service) rowForReference(ctx context.Context, reference string) (db.ManagedSecrets, error) {
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
func (s *Service) projectRow(ctx context.Context, projectID, reference string) (db.ManagedSecrets, error) {
	row, err := s.rowForReference(ctx, reference)
	if err != nil {
		return db.ManagedSecrets{}, err
	}
	if row.ProjectID != strings.TrimSpace(projectID) {
		return db.ManagedSecrets{}, ErrNotVisible
	}
	return row, nil
}

func nullable(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

func nullableInt(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: value != 0}
}

func agentUseEnded(value sql.NullString, now time.Time) bool {
	if !value.Valid {
		return false
	}
	deadline, err := time.Parse(time.RFC3339Nano, value.String)
	return err != nil || !now.Before(deadline)
}

func visible(row db.ManagedSecrets, projectID, chatSessionID string) error {
	if row.ProjectID != strings.TrimSpace(projectID) {
		return ErrNotVisible
	}
	if row.Scope == ScopeChat && (!row.ChatSessionID.Valid || row.ChatSessionID.String != strings.TrimSpace(chatSessionID)) {
		return ErrNotVisible
	}
	return nil
}
