package secretcap

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// AgentUseDeadline replaces or clears the agent-use deadline.
type AgentUseDeadline struct {
	At string
}

// UpdateRequest administers one capability. A nil field is left as it stands.
type UpdateRequest struct {
	ProjectID        string
	Reference        string
	Name             *string
	Purpose          *string
	AgentUseDeadline *AgentUseDeadline
	// Scope only widens from chat to project.
	Scope *string
}

// Update applies metadata changes to a non-revoked capability.
func (s *Service) Update(ctx context.Context, req UpdateRequest) (Metadata, error) {
	defer s.invalidateScreening(ctx, req.ProjectID)
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	row, err := s.projectRow(ctx, req.ProjectID, req.Reference)
	if err != nil {
		return Metadata{}, err
	}
	if row.RevokedAt.Valid {
		return Metadata{}, ErrRevoked
	}
	target, err := s.merge(row, req)
	if err != nil {
		return Metadata{}, err
	}
	changed, err := s.queries.UpdateManagedSecret(ctx, target)
	if err != nil {
		if row.Origin == OriginCookieJar && db.IsUniqueConstraint(err) {
			return Metadata{}, fmt.Errorf("%w: a cookie jar with this name already exists in the target scope", ErrInvalidUpdate)
		}
		return Metadata{}, err
	}
	if changed == 0 {
		// Revocation is the only guarded concurrent change.
		return Metadata{}, ErrRevoked
	}
	if target.Scope != row.Scope {
		if err := s.releaseChatCustody(ctx, row.ID); err != nil {
			return Metadata{}, err
		}
	}
	updated, err := s.queries.GetManagedSecret(ctx, row.ID)
	if err != nil {
		return Metadata{}, err
	}
	return s.metadataRow(ctx, updated)
}

// merge produces one complete update from a partial request.
func (s *Service) merge(row db.ManagedSecrets, req UpdateRequest) (db.UpdateManagedSecretParams, error) {
	target := db.UpdateManagedSecretParams{
		Name: row.Name, Purpose: row.Purpose, Scope: row.Scope,
		ChatSessionID: row.ChatSessionID, AgentUseEndsAt: row.AgentUseEndsAt,
		ID: row.ID, ProjectID: row.ProjectID,
	}
	if req.Name != nil {
		target.Name = strings.TrimSpace(*req.Name)
	}
	if req.Purpose != nil {
		target.Purpose = strings.TrimSpace(*req.Purpose)
	}
	if err := validateLabels(target.Name, target.Purpose, ErrInvalidUpdate); err != nil {
		return db.UpdateManagedSecretParams{}, err
	}
	if row.Origin == OriginCookieJar && !validJarName(target.Name) {
		return db.UpdateManagedSecretParams{}, ErrInvalidCookieJar
	}
	if req.AgentUseDeadline != nil {
		stamp, err := parseAgentUseDeadline(req.AgentUseDeadline.At, s.now())
		if err != nil {
			return db.UpdateManagedSecretParams{}, fmt.Errorf("%w: %w", ErrInvalidUpdate, err)
		}
		target.AgentUseEndsAt = nullable(stamp)
	}
	if err := applyScope(&target, row, req.Scope); err != nil {
		return db.UpdateManagedSecretParams{}, err
	}
	return target, nil
}

// applyScope requires a purpose when widening scope.
func applyScope(target *db.UpdateManagedSecretParams, row db.ManagedSecrets, scope *string) error {
	if scope == nil {
		return nil
	}
	next := strings.TrimSpace(*scope)
	if next == row.Scope {
		return nil
	}
	if next == ScopeChat {
		return fmt.Errorf("%w: a project capability cannot be narrowed to one chat", ErrInvalidUpdate)
	}
	if next != ScopeProject {
		return fmt.Errorf("%w: unknown scope %q", ErrInvalidUpdate, next)
	}
	if target.Purpose == "" {
		return fmt.Errorf("%w: purpose is required for project scope", ErrInvalidUpdate)
	}
	target.Scope = ScopeProject
	target.ChatSessionID = nullable("")
	return nil
}

// agentAuthoredOrigin reports origins created without naming a person.
func agentAuthoredOrigin(origin string) bool {
	return origin == OriginGenerated || origin == OriginDetected || origin == OriginCookieJar || origin == OriginTokenJar
}

// RevokeByAgent revokes a visible capability whose current bytes the host
// supplied. Custody, not origin, decides: a protected detection, a replaced
// value, and a held value all carry bytes a person handed over.
func (s *Service) RevokeByAgent(ctx context.Context, projectID, chatSessionID, reference string) (Metadata, error) {
	row, err := s.rowForReference(ctx, reference)
	if err != nil {
		return Metadata{}, err
	}
	if err := visible(row, projectID, chatSessionID); err != nil {
		return Metadata{}, err
	}
	return s.revokeRow(ctx, row, revoker{by: revokedByAgent, allowed: s.hostSupplied})
}

// hostSupplied refuses unless the current bytes have chat or host custody.
// No readable value means no custody to grant the agent authority.
func (s *Service) hostSupplied(ctx context.Context, row db.ManagedSecrets) error {
	current, ok, err := s.currentVersion(ctx, row.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: no readable value", ErrHumanAuthored)
	}
	entry, ok := s.values.get(current.ID)
	if !ok {
		return fmt.Errorf("%w: no readable value", ErrHumanAuthored)
	}
	if entry.Custody != CustodyChat && entry.Custody != CustodyHost {
		return fmt.Errorf("%w: %s custody", ErrHumanAuthored, entry.Custody)
	}
	return nil
}

// RevokeProject disables any capability in the project on a person's request.
func (s *Service) RevokeProject(ctx context.Context, projectID, reference, personID string) (Metadata, error) {
	personID = strings.TrimSpace(personID)
	if personID == "" {
		return Metadata{}, fmt.Errorf("revoke managed secret: the revoking person is required")
	}
	row, err := s.projectRow(ctx, projectID, reference)
	if err != nil {
		return Metadata{}, err
	}
	return s.revokeRow(ctx, row, revoker{by: revokedByPerson, personID: personID})
}

const (
	revokedByPerson = "person"
	revokedByAgent  = "agent"
)

// revoker is who ended a capability: a person, or the agent that authored it.
// allowed, when set, decides authority under the mutation lock so a concurrent
// replace or hold cannot change custody between the check and the revoke.
type revoker struct {
	by       string
	personID string
	allowed  func(context.Context, db.ManagedSecrets) error
}

// revokeRow serializes terminal state with other mutations.
func (s *Service) revokeRow(ctx context.Context, row db.ManagedSecrets, by revoker) (Metadata, error) {
	defer s.invalidateScreening(ctx, row.ProjectID)
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	when := db.FormatTime(s.now())
	if !row.RevokedAt.Valid && by.allowed != nil {
		if err := by.allowed(ctx, row); err != nil {
			return Metadata{}, err
		}
	}
	if !row.RevokedAt.Valid {
		if _, err := s.queries.RevokeManagedSecret(ctx, db.RevokeManagedSecretParams{
			RevokedAt: nullable(when), RevokedBy: nullable(by.by), RevokedByPersonID: nullable(by.personID),
			ID: row.ID, ProjectID: row.ProjectID,
		}); err != nil {
			return Metadata{}, err
		}
		row.RevokedAt = nullable(when)
		row.RevokedBy, row.RevokedByPersonID = nullable(by.by), nullable(by.personID)
		s.retireDurable(func(_ string, owner secretIdentity) bool { return owner.id == row.ID })
	}
	return s.metadataRow(ctx, row)
}

// HoldValue re-records a host-supplied current value as person-held, so every
// release, including the generating chat's own, needs a reviewed handoff and
// that person's unlock. It is one-way, like promotion; a value already held
// returns unchanged. A jar never takes a reviewed release, and a marked
// file's bytes stay governed by the file, so neither can be held.
func (s *Service) HoldValue(ctx context.Context, projectID, reference string) (Metadata, error) {
	defer s.invalidateScreening(ctx, projectID)
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	row, err := s.projectRow(ctx, projectID, reference)
	if err != nil {
		return Metadata{}, err
	}
	if row.RevokedAt.Valid {
		return Metadata{}, ErrRevoked
	}
	if row.Origin == OriginCookieJar || row.Origin == OriginTokenJar {
		return Metadata{}, fmt.Errorf("%w: a jar reaches services through the jar; revoke it instead", ErrInvalidUpdate)
	}
	current, ok, err := s.currentVersion(ctx, row.ID)
	if err != nil {
		return Metadata{}, err
	}
	if !ok {
		return Metadata{}, ErrValueMissing
	}
	entry, ok := s.values.get(current.ID)
	if !ok {
		return Metadata{}, ErrValueMissing
	}
	switch entry.Custody {
	case CustodyPerson:
		return s.metadataRow(ctx, row)
	case CustodyFile:
		return Metadata{}, fmt.Errorf("%w: a value marked in a project file is governed by that file", ErrInvalidUpdate)
	}
	entry.Custody = CustodyPerson
	if err := s.values.put(current.ID, entry); err != nil {
		return Metadata{}, fmt.Errorf("record held custody: %w", err)
	}
	return s.metadataRow(ctx, row)
}

// releaseChatCustody re-records a promoted value as the host's: once later
// chats can spend it, no one chat alone has held it. A failed rewrite leaves
// the entry bound to its first chat, which only withholds the silent release.
func (s *Service) releaseChatCustody(ctx context.Context, secretID string) error {
	current, ok, err := s.currentVersion(ctx, secretID)
	if err != nil || !ok {
		return err
	}
	entry, ok := s.values.get(current.ID)
	if !ok || entry.Custody != CustodyChat {
		return nil
	}
	entry.Custody = CustodyHost
	if err := s.values.put(current.ID, entry); err != nil {
		return fmt.Errorf("record promoted custody: %w", err)
	}
	return nil
}
