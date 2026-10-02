package secretcap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// ReplaceValueRequest names a capability and its next value.
type ReplaceValueRequest struct {
	ProjectID, Reference, Value string
}

// ReplaceValue changes the current value while retaining screening history.
func (s *Service) ReplaceValue(ctx context.Context, req ReplaceValueRequest) (Metadata, error) {
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
	// A jar's values reach services through the jar, never through a reviewed
	// release, so its document stays host-maintained; a person ends one.
	if row.Origin == OriginCookieJar || row.Origin == OriginTokenJar {
		return Metadata{}, fmt.Errorf("%w: a jar is kept from service responses; revoke it instead", ErrInvalidPut)
	}
	if err := validateValue(req.Value); err != nil {
		return Metadata{}, err
	}
	// Every replacement creates a version, so the response does not disclose value equality.
	current, hasCurrent, err := s.currentVersion(ctx, row.ID)
	if err != nil {
		return Metadata{}, err
	}
	// A person supplied the replacement bytes, whatever produced the first value.
	if err := s.replaceOnto(ctx, row, current, hasCurrent, CustodyPerson, req.Value, nil); err != nil {
		return Metadata{}, err
	}
	s.screeningGeneration.Add(1)
	return s.metadataRow(ctx, row)
}

// replaceOnto swaps the current version in one metadata transaction. custody
// names who supplied the new bytes.
func (s *Service) replaceOnto(
	ctx context.Context, row db.ManagedSecrets, current db.ManagedSecretVersions, hasCurrent bool,
	custody Custody, value string, record func(*db.Queries) error,
) error {
	next, err := s.queries.NextManagedSecretVersion(ctx, row.ID)
	if err != nil {
		return err
	}
	valueID := uuid.NewString()
	s.protectDurableVersion(valueID, identityOf(row), value)
	if err := s.values.put(valueID, protectedValue{Custody: custody, Value: value}); err != nil {
		return fmt.Errorf("store managed secret: %w", err)
	}
	stamp := db.FormatTime(s.now())
	err = s.inTx(ctx, func(q *db.Queries) error {
		if hasCurrent {
			if err := q.RetireManagedSecretVersion(ctx, db.RetireManagedSecretVersionParams{
				RetiredAt: nullable(stamp), ID: current.ID,
			}); err != nil {
				return fmt.Errorf("retire managed secret version: %w", err)
			}
		}
		if err := q.CreateManagedSecretVersion(ctx, db.CreateManagedSecretVersionParams{
			ID: valueID, SecretID: row.ID, Version: next, CreatedAt: stamp,
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
	if hasCurrent {
		s.retireDurable(func(versionID string, _ secretIdentity) bool { return versionID == current.ID })
	}
	return nil
}

func (s *Service) currentVersion(ctx context.Context, secretID string) (db.ManagedSecretVersions, bool, error) {
	row, err := s.queries.GetCurrentManagedSecretVersion(ctx, secretID)
	switch {
	case err == nil:
		return row, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return db.ManagedSecretVersions{}, false, nil
	default:
		return db.ManagedSecretVersions{}, false, err
	}
}

// RememberProjectValues refreshes screening with every retained project value.
func (s *Service) RememberProjectValues(ctx context.Context, projectID, chatSessionID string) error {
	projectID = strings.TrimSpace(projectID)
	chatSessionID = strings.TrimSpace(chatSessionID)
	if projectID == "" || chatSessionID == "" || s.remember == nil {
		return nil
	}
	values, err := s.ScreeningValues(ctx, projectID, chatSessionID)
	if err != nil {
		return err
	}
	s.remember(chatSessionID, values)
	return nil
}

// ScreeningValues returns project evidence for a chat's screens. A reference
// attaches only to a capability the chat may spend; an empty chat admits only
// project references.
func (s *Service) ScreeningValues(ctx context.Context, projectID, chatSessionID string) ([]secretmatch.Remembered, error) {
	projectID = strings.TrimSpace(projectID)
	now := s.now()
	return s.screeningValues(ctx, projectID, func(row db.ManagedSecrets) bool {
		return visible(row, projectID, chatSessionID) == nil && !agentUseEnded(row.AgentUseEndsAt, now)
	})
}

// ReviewScreeningValues returns project evidence for people reviewing project
// text. Every live capability carries its reference, whichever chat it serves.
func (s *Service) ReviewScreeningValues(ctx context.Context, projectID string) ([]secretmatch.Remembered, error) {
	return s.screeningValues(ctx, strings.TrimSpace(projectID), func(db.ManagedSecrets) bool { return true })
}

// screeningValues retains every stored version: retired bytes stay evidence.
func (s *Service) screeningValues(
	ctx context.Context, projectID string, spends func(db.ManagedSecrets) bool,
) ([]secretmatch.Remembered, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if projectID == "" {
		return nil, nil
	}
	rows, err := s.queries.ListProjectManagedSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	versions, err := s.queries.ListProjectManagedSecretVersions(ctx, projectID)
	if err != nil {
		return nil, err
	}
	bySecret := make(map[string][]db.ManagedSecretVersions, len(rows))
	for _, version := range versions {
		bySecret[version.SecretID] = append(bySecret[version.SecretID], version)
	}
	var evidence []secretmatch.Remembered
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		owner, spent := identityOf(row), spends(row)
		for _, version := range bySecret[row.ID] {
			entry, ok := s.values.get(version.ID)
			if !ok {
				continue
			}
			evidence = append(evidence, versionEvidence(owner, entry.Value, standingOf(row, version, spent))...)
		}
	}
	return evidence, nil
}

// rememberValue offers a value its chat just spent back to that chat's screens.
func (s *Service) rememberValue(chatSessionID, name, id, value string) {
	if s.remember == nil || strings.TrimSpace(chatSessionID) == "" {
		return
	}
	s.remember(chatSessionID, []secretmatch.Remembered{managedScreeningValue(name, id, value, standingReferenced)})
}

// ProjectRemoval returns value cleanup to run after project deletion.
func (s *Service) ProjectRemoval(ctx context.Context, projectID string) (func() error, error) {
	ids, err := s.queries.ListManagedSecretVersionIDsForProject(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	return func() error {
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
		for _, id := range ids {
			if err := s.values.delete(id); err != nil {
				return fmt.Errorf("remove project secret value: %w", err)
			}
			s.forgetDurableVersion(id)
		}
		return nil
	}, nil
}

// DiscardCreated removes a newly created capability after its operation fails.
func (s *Service) DiscardCreated(ctx context.Context, projectID, reference string) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	id, err := ParseReference(reference)
	if err != nil {
		return err
	}
	row, err := s.queries.GetManagedSecret(ctx, id)
	if err != nil {
		return err
	}
	if row.ProjectID != strings.TrimSpace(projectID) {
		return ErrNotVisible
	}
	ids, err := s.queries.ListManagedSecretVersionIDsForSecret(ctx, id)
	if err != nil {
		return err
	}
	removed, err := s.queries.DeleteManagedSecret(ctx, db.DeleteManagedSecretParams{
		ID: id, ProjectID: row.ProjectID,
	})
	if err != nil {
		return err
	}
	if removed != 1 {
		return ErrNotFound
	}
	for _, valueID := range ids {
		if err := s.values.delete(valueID); err != nil {
			return fmt.Errorf("remove discarded secret value: %w", err)
		}
		s.forgetDurableVersion(valueID)
	}
	return nil
}

// Reconcile deletes protected values with no version to name them.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	ids, err := s.queries.ListManagedSecretVersionIDs(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		known[id] = struct{}{}
	}
	for _, id := range s.values.ids() {
		if _, ok := known[id]; ok {
			continue
		}
		if err := s.values.delete(id); err != nil {
			return fmt.Errorf("remove orphaned secret value: %w", err)
		}
	}
	return s.restoreDurableEvidence(ctx)
}

// RunMaintenance periodically removes value-store orphans.
func (s *Service) RunMaintenance(ctx context.Context) error {
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		if err := s.Reconcile(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
