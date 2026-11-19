// Package secretcap manages protected secret capabilities.
package secretcap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	// ErrValueChanged: the value a reveal challenge named was replaced before
	// the reveal completed.
	ErrValueChanged = errors.New("managed secret changed during presence verification")
)

// RememberFunc admits a managed value to exact-match secret screening.
type RememberFunc func(chatSessionID string, values []secretmatch.Remembered)

// Service manages capability creation, use, and lifecycle.
type Service struct {
	repository  *repository
	Jars        *Jars
	Reveals     *Reveals
	unlocks     *presence.Unlocks
	fingerprint func(string) secretmatch.SecretFingerprint
}

// Jars applies scoped cookie and token operations with their save receipts.
type Jars struct{ repository *repository }

// Reveals verifies presence against a value version and records disclosure.
type Reveals struct {
	repository *repository
	broker     *presence.Broker
}

// AddScreeningInvalidationObserver installs a boot-time observer for protected-value mutations.
func (s *Service) AddScreeningInvalidationObserver(fn func(context.Context, string)) {
	s.repository.onScreeningInvalidated = append(s.repository.onScreeningInvalidated, fn)
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
	repository := &repository{handle: database, queries: db.New(database), values: vaultValues{store: values}, remember: remember, now: time.Now}
	return &Service{repository: repository, Jars: &Jars{repository: repository}, Reveals: &Reveals{repository: repository}}
}

// SetPresence installs the broker that verifies reveals.
func (s *Reveals) SetPresence(broker *presence.Broker) { s.broker = broker }

// SetUnlocks installs the chats' unlocks a held value's handoff needs.
func (s *Service) SetUnlocks(unlocks *presence.Unlocks) { s.unlocks = unlocks }

// SetFingerprinter installs the screen's identity for exact bytes, so a
// resolution can name which screened values a person holds.
func (s *Service) SetFingerprinter(fp *secretmatch.Fingerprinter) { s.fingerprint = fp.Fingerprint }

// ScreeningGeneration changes when screening gains protected bytes.
func (s *Service) ScreeningGeneration() uint64 {
	if s == nil {
		return 0
	}
	return s.repository.screeningGeneration.Load()
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
}

// List returns value-free capabilities visible from a chat.
func (s *Service) List(ctx context.Context, projectID, chatSessionID string) ([]Metadata, error) {
	projectID = strings.TrimSpace(projectID)
	rows, err := s.repository.queries.ListVisibleManagedSecrets(ctx, db.ListVisibleManagedSecretsParams{
		ProjectID: projectID, ChatSessionID: nullable(strings.TrimSpace(chatSessionID)),
	})
	if err != nil {
		return nil, err
	}
	return s.repository.metadataList(ctx, projectID, rows)
}

// ListProject returns project metadata without protected values.
func (s *Service) ListProject(ctx context.Context, projectID string) ([]Metadata, error) {
	projectID = strings.TrimSpace(projectID)
	rows, err := s.repository.queries.ListProjectManagedSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return s.repository.metadataList(ctx, projectID, rows)
}

// Describe returns value-free metadata visible to a chat.
func (s *Service) Describe(ctx context.Context, projectID, chatSessionID, reference string) (Metadata, error) {
	row, err := s.repository.rowForReference(ctx, reference)
	if err != nil {
		return Metadata{}, err
	}
	if err := visible(row, projectID, chatSessionID); err != nil {
		return Metadata{}, err
	}
	return s.repository.metadataRow(ctx, row)
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
