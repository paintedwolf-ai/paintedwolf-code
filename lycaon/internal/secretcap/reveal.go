package secretcap

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/presence"
)

const revealRemaskAfter = 30 * time.Second

// RevealChallenge is a value-free, single-use presence challenge for one value.
type RevealChallenge struct {
	ID           string
	ProofPayload string
	Prompt       string
	Version      int64
	ExpiresAt    string
}

// RevealResult carries a presence-verified disclosure to the person's own view.
type RevealResult struct {
	Value              credentialstore.SecretValue
	Version            int64
	RevealedAt         string
	RemaskAfterSeconds int64
}

// revealSubject is what a reveal challenge signs.
type revealSubject struct {
	ProjectID string `json:"project_id"`
	SecretID  string `json:"secret_id"`
	Version   int64  `json:"version"`
}

// PresenceAvailable reports whether this device can verify presence.
func (s *Service) PresenceAvailable() bool { return s != nil && s.presence.Available() }

// BeginReveal binds presence to one value version, window, and person.
func (s *Service) BeginReveal(
	ctx context.Context, projectID, reference, windowLabel, personID string,
) (RevealChallenge, error) {
	row, err := s.projectRow(ctx, projectID, reference)
	if err != nil {
		return RevealChallenge{}, err
	}
	if !s.PresenceAvailable() {
		return RevealChallenge{}, presence.ErrUnavailable
	}
	if row.RevokedAt.Valid {
		return RevealChallenge{}, ErrRevoked
	}
	current, ok, err := s.currentVersion(ctx, row.ID)
	if err != nil {
		return RevealChallenge{}, err
	}
	if !ok {
		return RevealChallenge{}, ErrValueMissing
	}
	if _, ok := s.values.get(current.ID); !ok {
		return RevealChallenge{}, ErrValueMissing
	}
	challenge, err := s.presence.Begin(presence.Claim{
		Purpose: presence.PurposeReveal, PersonID: personID, WindowLabel: windowLabel, Key: row.ID,
		Subject: revealSubject{ProjectID: row.ProjectID, SecretID: row.ID, Version: current.Version},
	})
	if err != nil {
		return RevealChallenge{}, err
	}
	return RevealChallenge{
		ID: challenge.ID, ProofPayload: challenge.ProofPayload,
		Prompt: revealPrompt(row.Name, row.Purpose), Version: current.Version,
		ExpiresAt: challenge.ExpiresAt,
	}, nil
}

// CompleteReveal consumes one challenge and records the reveal before
// returning the value to the person who began it.
func (s *Service) CompleteReveal(
	ctx context.Context, projectID, reference, personID string, proof presence.Proof,
) (RevealResult, error) {
	row, err := s.projectRow(ctx, projectID, reference)
	if err != nil {
		return RevealResult{}, err
	}
	verified, err := s.presence.Complete(proof, presence.PurposeReveal, personID)
	if err != nil {
		return RevealResult{}, err
	}
	var subject revealSubject
	if err := json.Unmarshal(verified.Subject, &subject); err != nil ||
		subject.ProjectID != strings.TrimSpace(projectID) || subject.SecretID != row.ID {
		return RevealResult{}, presence.ErrDenied
	}
	if row.RevokedAt.Valid {
		return RevealResult{}, ErrRevoked
	}
	current, ok, err := s.currentVersion(ctx, row.ID)
	if err != nil {
		return RevealResult{}, err
	}
	if !ok {
		return RevealResult{}, ErrValueMissing
	}
	if current.Version != subject.Version {
		return RevealResult{}, ErrValueChanged
	}
	entry, ok := s.values.get(current.ID)
	if !ok {
		return RevealResult{}, ErrValueMissing
	}
	revealedAt := db.FormatTime(s.now())
	if err := s.queries.CreateManagedSecretReveal(ctx, db.CreateManagedSecretRevealParams{
		ID: verified.ChallengeID, SecretID: row.ID, Version: current.Version,
		Authenticator: verified.Authenticator, WindowLabel: verified.WindowLabel, PersonID: verified.PersonID,
		RevealedAt: revealedAt,
	}); err != nil {
		return RevealResult{}, fmt.Errorf("record managed secret reveal: %w", err)
	}
	return RevealResult{
		Value: credentialstore.SecretValue(entry.Value), Version: current.Version, RevealedAt: revealedAt,
		RemaskAfterSeconds: int64(revealRemaskAfter / time.Second),
	}, nil
}

func revealPrompt(name, purpose string) string {
	name = presence.PromptText(name)
	purpose = presence.PromptText(purpose)
	if purpose == "" {
		return fmt.Sprintf("Reveal the current value for %q in Painted Wolf Code.", name)
	}
	return fmt.Sprintf("Reveal the current value for %q in Painted Wolf Code. Purpose: %s", name, purpose)
}
