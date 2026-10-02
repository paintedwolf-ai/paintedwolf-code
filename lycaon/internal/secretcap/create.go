package secretcap

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// GenerateRequest describes new random key material.
type GenerateRequest struct {
	ProjectID, ChatSessionID, SessionID, OperationID string
	Name, Purpose, Scope, Format                     string
	Bytes                                            int
	AgentUseTTL                                      time.Duration
}

// PutRequest stores a supplied value as a new capability.
type PutRequest struct {
	ProjectID, ChatSessionID, SessionID, OperationID string
	Name, Purpose, Scope, Origin, Value              string
	// PersonID is the person who supplied or marked the value. Agent-authored
	// origins carry none.
	PersonID    string
	AgentUseTTL time.Duration
	// AgentUseEndsAt preserves an already-validated absolute deadline.
	AgentUseEndsAt string
	Format         string
	EntropyBits    int64
}

// PutResult is the stored or idempotently matched capability.
type PutResult struct {
	Metadata Metadata
	// Created is true only when this call minted the capability.
	Created bool
}

// CreateSettingsSecretRequest is a project secret a person entered in settings.
type CreateSettingsSecretRequest struct {
	ProjectID, OperationID, PersonID string
	Name, Purpose, Value             string
	AgentUseEndsAt                   string
}

// Generate mints random key material as a new capability.
func (s *Service) Generate(ctx context.Context, req GenerateRequest) (Metadata, error) {
	req = normalizeGenerate(req)
	if err := validateGenerate(req); err != nil {
		return Metadata{}, err
	}
	value, entropyBits, err := generateValue(req.Format, req.Bytes)
	if err != nil {
		return Metadata{}, err
	}
	result, err := s.Put(ctx, PutRequest{
		ProjectID: req.ProjectID, ChatSessionID: req.ChatSessionID, SessionID: req.SessionID,
		OperationID: req.OperationID, Name: req.Name, Purpose: req.Purpose, Scope: req.Scope,
		Origin: OriginGenerated, Value: value, AgentUseTTL: req.AgentUseTTL, Format: req.Format, EntropyBits: entropyBits,
	})
	if errors.Is(err, ErrInvalidPut) {
		return Metadata{}, fmt.Errorf("%w: %w", ErrInvalidGenerate, err)
	}
	return result.Metadata, err
}

// CreateSettingsSecret stores a project-scoped value entered in settings.
func (s *Service) CreateSettingsSecret(ctx context.Context, req CreateSettingsSecretRequest) (Metadata, error) {
	deadlineStamp, err := parseAgentUseDeadline(req.AgentUseEndsAt, s.now())
	if err != nil {
		return Metadata{}, fmt.Errorf("%w: %w", ErrInvalidPut, err)
	}
	result, err := s.Put(ctx, PutRequest{
		ProjectID: req.ProjectID, OperationID: req.OperationID, PersonID: req.PersonID, Name: req.Name,
		Purpose: req.Purpose, Scope: ScopeProject, Origin: OriginSettingsEntered,
		Value: req.Value, AgentUseEndsAt: deadlineStamp,
	})
	return result.Metadata, err
}

// Put stores a value as a new capability, idempotent per operation id.
func (s *Service) Put(ctx context.Context, req PutRequest) (PutResult, error) {
	defer s.invalidateScreening(ctx, req.ProjectID)
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	req = normalizePut(req)
	if err := validatePut(req); err != nil {
		return PutResult{}, err
	}
	// Idempotency is per operation, never per value.
	if existing, ok, err := s.existingByOperation(ctx, req); err != nil {
		return PutResult{}, err
	} else if ok {
		meta, metaErr := s.metadataRow(ctx, existing)
		return PutResult{Metadata: meta}, metaErr
	}
	if req.Origin == OriginFileMarked && s.Protects(req.ProjectID, req.Value) {
		return PutResult{}, ErrAlreadyProtected
	}

	id := uuid.NewString()
	now := s.now().UTC()
	if err := s.createWithFirstVersion(ctx, id, req, now, nil); err != nil {
		return PutResult{}, err
	}
	s.screeningGeneration.Add(1)
	s.rememberValue(req.ChatSessionID, req.Name, id, req.Value)
	// Return the reference if metadata lookup fails.
	created := PutResult{Metadata: Metadata{Reference: secretmatch.ReferenceToken(id)}, Created: true}
	row, err := s.queries.GetManagedSecret(ctx, id)
	if err != nil {
		return created, err
	}
	meta, err := s.metadataRow(ctx, row)
	if err != nil {
		return created, err
	}
	created.Metadata = meta
	return created, nil
}

// createWithFirstVersion records metadata only after storing its value.
func (s *Service) createWithFirstVersion(ctx context.Context, id string, req PutRequest, now time.Time, record func(*db.Queries) error) error {
	valueID := uuid.NewString()
	// Publish protection before the metadata writer can make the value visible.
	// A failed mint may still have supplied bytes to a diagnostic or tool result.
	owner := secretIdentity{id: id, projectID: req.ProjectID, name: req.Name, origin: req.Origin}
	s.protectDurableVersion(valueID, owner, req.Value)
	if err := s.values.put(valueID, firstEntry(req)); err != nil {
		return fmt.Errorf("store managed secret: %w", err)
	}
	stamp := db.FormatTime(now)
	params := db.CreateManagedSecretParams{
		ID: id, ProjectID: req.ProjectID, Scope: req.Scope, Name: req.Name,
		Purpose: req.Purpose, Origin: req.Origin, Format: nullable(req.Format),
		EntropyBits: nullableInt(req.EntropyBits), OperationID: req.OperationID,
		CreatedAt: stamp, CreatedBySessionID: nullable(req.SessionID),
		CreatedByPersonID: nullable(req.PersonID),
	}
	if req.Scope == ScopeChat {
		params.ChatSessionID = nullable(req.ChatSessionID)
	}
	switch {
	case req.AgentUseEndsAt != "":
		params.AgentUseEndsAt = nullable(req.AgentUseEndsAt)
	case req.AgentUseTTL > 0:
		params.AgentUseEndsAt = nullable(now.Add(req.AgentUseTTL).Format(time.RFC3339Nano))
	}
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.CreateManagedSecret(ctx, params); err != nil {
			return fmt.Errorf("record managed secret: %w", err)
		}
		if err := q.CreateManagedSecretVersion(ctx, db.CreateManagedSecretVersionParams{
			ID: valueID, SecretID: id, Version: 1, CreatedAt: stamp,
		}); err != nil {
			return err
		}
		if record != nil {
			return record(q)
		}
		return nil
	})
	if err != nil {
		_ = s.values.delete(valueID)
		return err
	}
	return nil
}

// existingByOperation resolves session and project idempotency keys.
func (s *Service) existingByOperation(ctx context.Context, req PutRequest) (db.ManagedSecrets, bool, error) {
	var row db.ManagedSecrets
	var err error
	if req.SessionID == "" {
		row, err = s.queries.GetSessionlessManagedSecretByOperation(ctx, db.GetSessionlessManagedSecretByOperationParams{
			ProjectID: req.ProjectID, OperationID: req.OperationID,
		})
	} else {
		row, err = s.queries.GetManagedSecretByOperation(ctx, db.GetManagedSecretByOperationParams{
			ProjectID: req.ProjectID, CreatedBySessionID: nullable(req.SessionID), OperationID: req.OperationID,
		})
	}
	switch {
	case err == nil:
		return row, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return db.ManagedSecrets{}, false, nil
	default:
		return db.ManagedSecrets{}, false, err
	}
}

// inTx commits one metadata mutation. Vault writes are reconciled separately.
func (s *Service) inTx(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.handle.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func normalizePut(req PutRequest) PutRequest {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.ChatSessionID = strings.TrimSpace(req.ChatSessionID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.PersonID = strings.TrimSpace(req.PersonID)
	req.Name = strings.TrimSpace(req.Name)
	req.Purpose = strings.TrimSpace(req.Purpose)
	req.Scope = strings.TrimSpace(req.Scope)
	req.Origin = strings.TrimSpace(req.Origin)
	req.Format = strings.TrimSpace(req.Format)
	if req.Origin == OriginDetected {
		// Detector descriptions and source labels are not bounded form inputs.
		req.Name = boundedDetectedLabel(req.Name, maxNameRunes)
		req.Purpose = boundedDetectedLabel(req.Purpose, maxPurposeRunes)
		if req.Name == "" {
			req.Name = "Detected credential"
		}
	}
	if req.Scope == "" {
		req.Scope = ScopeChat
	}
	return req
}

func validatePut(req PutRequest) error {
	if req.ProjectID == "" || req.OperationID == "" {
		return invalidField(ErrInvalidPut, "project_id/operation_id", "host identities are required")
	}
	if err := validatePutIdentity(req); err != nil {
		return err
	}
	if err := validateLabels(req.Name, req.Purpose, ErrInvalidPut); err != nil {
		return err
	}
	if req.Scope == ScopeProject && req.Purpose == "" {
		return invalidField(ErrInvalidPut, "purpose", "required for project scope")
	}
	if err := validateValue(req.Value); err != nil {
		return err
	}
	if err := validatePutAgentUse(req); err != nil {
		return err
	}
	return validatePutProvenance(req)
}

// validatePutAgentUse accepts one expression of the agent-use limit.
func validatePutAgentUse(req PutRequest) error {
	if req.AgentUseTTL != 0 && req.AgentUseEndsAt != "" {
		return invalidField(ErrInvalidPut, "agent_use", "choose a lifetime or a deadline")
	}
	if req.AgentUseTTL < 0 || req.AgentUseTTL > maxAgentUseLifetime {
		return invalidField(ErrInvalidPut, "agent_use_ttl_seconds", "must be nonnegative and at most one year")
	}
	if req.AgentUseEndsAt == "" {
		return nil
	}
	// Parsing already bounds absolute deadlines.
	if _, err := time.Parse(time.RFC3339Nano, req.AgentUseEndsAt); err != nil {
		return fmt.Errorf("%w: agent-use deadline must be an RFC 3339 timestamp", ErrInvalidPut)
	}
	return nil
}

// validatePutIdentity enforces chat and project provenance.
func validatePutIdentity(req PutRequest) error {
	if sessionlessOrigin(req.Origin) {
		if req.SessionID != "" || req.ChatSessionID != "" {
			return fmt.Errorf("%w: a %s value is a project act and names no session", ErrInvalidPut, req.Origin)
		}
		if req.Scope != ScopeProject {
			return fmt.Errorf("%w: a %s value takes project scope", ErrInvalidPut, req.Origin)
		}
		return nil
	}
	if req.SessionID == "" {
		return invalidField(ErrInvalidPut, "session_id", "host session identity is required")
	}
	if req.Scope == ScopeChat && req.ChatSessionID == "" {
		return invalidField(ErrInvalidPut, "chat_session_id", "host chat identity is required for chat scope")
	}
	if req.Scope != ScopeChat && req.Scope != ScopeProject {
		return invalidField(ErrInvalidPut, "scope", "must name chat or project")
	}
	return nil
}

func validatePutProvenance(req PutRequest) error {
	switch req.Origin {
	case OriginGenerated:
		if req.EntropyBits < 128 || req.EntropyBits > 1024 {
			return fmt.Errorf("%w: generated entropy is outside its limit", ErrInvalidPut)
		}
		switch req.Format {
		case FormatBase64URL, FormatHex, FormatAlphanumeric:
		default:
			return invalidField(ErrInvalidPut, "format", "must name base64url, hex, or alphanumeric")
		}
	case OriginAskUserResponse, OriginDetected, OriginFileMarked, OriginComposerMarked, OriginSettingsEntered, OriginCookieJar, OriginTokenJar:
		if req.Format != "" || req.EntropyBits != 0 {
			return fmt.Errorf("%w: imported values cannot claim generated metadata", ErrInvalidPut)
		}
	default:
		return fmt.Errorf("%w: unknown origin %q", ErrInvalidPut, req.Origin)
	}
	if agentAuthoredOrigin(req.Origin) == (req.PersonID != "") {
		return fmt.Errorf("%w: a %s value names a person exactly when a person supplied it", ErrInvalidPut, req.Origin)
	}
	return nil
}

// sessionlessOrigin reports project acts without a creating chat.
func sessionlessOrigin(origin string) bool {
	return origin == OriginFileMarked || origin == OriginSettingsEntered
}

const maxNameRunes = 80
const maxPurposeRunes = 240

func boundedDetectedLabel(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return value
}

func validateLabels(name, purpose string, kind error) error {
	if utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > maxNameRunes || utf8.RuneCountInString(purpose) > maxPurposeRunes {
		return invalidField(kind, "name/purpose", fmt.Sprintf(
			"name must contain 1–%d characters; purpose permits at most %d", maxNameRunes, maxPurposeRunes))
	}
	return nil
}

// validateValue applies the screening minimum in runes and the storage maximum in bytes.
func validateValue(value string) error {
	if len(value) > secretmatch.MaxSecretBytes {
		return fmt.Errorf("%w: value must be at most %d bytes", ErrInvalidPut, secretmatch.MaxSecretBytes)
	}
	if utf8.RuneCountInString(value) < secretmatch.MinManagedSecretRunes {
		return fmt.Errorf("%w: %w", ErrInvalidPut, ErrValueTooShort)
	}
	return nil
}

// parseAgentUseDeadline validates an optional deadline.
func parseAgentUseDeadline(value string, now time.Time) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", fmt.Errorf("agent-use deadline must be an RFC 3339 timestamp")
	}
	deadline = deadline.UTC()
	if !deadline.After(now) {
		return "", fmt.Errorf("agent-use deadline must be in the future")
	}
	if deadline.After(now.Add(maxAgentUseLifetime)) {
		return "", fmt.Errorf("agent-use deadline must not exceed one year from now")
	}
	return deadline.Format(time.RFC3339Nano), nil
}

func normalizeGenerate(req GenerateRequest) GenerateRequest {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.ChatSessionID = strings.TrimSpace(req.ChatSessionID)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.Name = strings.TrimSpace(req.Name)
	req.Purpose = strings.TrimSpace(req.Purpose)
	req.Scope = strings.TrimSpace(req.Scope)
	req.Format = strings.TrimSpace(req.Format)
	if req.Scope == "" {
		req.Scope = ScopeChat
	}
	if req.Format == "" {
		req.Format = FormatBase64URL
	}
	if req.Bytes == 0 {
		req.Bytes = 32
	}
	return req
}

func validateGenerate(req GenerateRequest) error {
	if req.Bytes < 16 || req.Bytes > 128 {
		return invalidField(ErrInvalidGenerate, "bytes", "must be between 16 and 128")
	}
	probe := PutRequest{
		ProjectID: req.ProjectID, ChatSessionID: req.ChatSessionID, SessionID: req.SessionID,
		OperationID: req.OperationID, Name: req.Name, Purpose: req.Purpose, Scope: req.Scope,
		Origin: OriginGenerated, Value: "generated-value-probe", AgentUseTTL: req.AgentUseTTL,
		Format: req.Format, EntropyBits: int64(req.Bytes * 8),
	}
	if err := validatePut(probe); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidGenerate, err)
	}
	return nil
}

func generateValue(format string, byteCount int) (string, int64, error) {
	raw := make([]byte, byteCount)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, fmt.Errorf("generate random secret: %w", err)
	}
	switch format {
	case FormatBase64URL:
		return base64.RawURLEncoding.EncodeToString(raw), int64(byteCount * 8), nil
	case FormatHex:
		return hex.EncodeToString(raw), int64(byteCount * 8), nil
	case FormatAlphanumeric:
		const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
		charCount := (byteCount*8 + 4) / 5
		out := make([]byte, charCount)
		for i := range out {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", 0, fmt.Errorf("generate random secret: %w", err)
			}
			out[i] = alphabet[n.Int64()]
		}
		return string(out), int64(byteCount * 8), nil
	default:
		return "", 0, fmt.Errorf("unknown secret format %q", format)
	}
}
