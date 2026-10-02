package secretcap

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// ReleaseScope is how long a presence-verified release keeps covering its
// values and recipients.
type ReleaseScope string

const (
	ReleaseOnce    ReleaseScope = "once"
	ReleaseChat    ReleaseScope = "chat"
	ReleaseProject ReleaseScope = "project"
)

// ReleaseRecord is one presence-verified release of person-held values.
type ReleaseRecord struct {
	AttestationID string
	CheckpointID  string
	Scope         ReleaseScope
	Recipients    []secretmatch.Recipient
	Held          []HeldValue
	Authenticator string
	WindowLabel   string
	PersonID      string
	AttestedAt    time.Time
}

// RecordRelease writes one attestation row per released value inside the
// approval's own transaction, so the decision and its audit commit together.
func RecordRelease(ctx context.Context, tx *sql.Tx, record ReleaseRecord) error {
	switch record.Scope {
	case ReleaseOnce, ReleaseChat, ReleaseProject:
	default:
		return fmt.Errorf("record managed secret release: unknown scope %q", record.Scope)
	}
	queries := db.New(tx)
	recipients := useRecipientsJSON(record.Recipients)
	attestedAt := db.FormatTime(record.AttestedAt)
	for _, held := range record.Held {
		if err := queries.CreateManagedSecretAttestation(ctx, db.CreateManagedSecretAttestationParams{
			ID: uuid.NewString(), AttestationID: record.AttestationID, SecretID: held.SecretID, Version: held.Version,
			Purpose: string(presence.PurposeRelease), CheckpointID: nullable(record.CheckpointID),
			RecipientsJson: recipients, ReleaseScope: nullable(string(record.Scope)),
			Authenticator: record.Authenticator, WindowLabel: record.WindowLabel, PersonID: record.PersonID,
			AttestedAt: attestedAt,
		}); err != nil {
			return fmt.Errorf("record managed secret release: %w", err)
		}
	}
	return nil
}

// Attestation is one presence-verified disclosure of a value.
type Attestation struct {
	AttestationID string         `json:"attestation_id"`
	Purpose       string         `json:"purpose"`
	Version       int64          `json:"version"`
	CheckpointID  string         `json:"checkpoint_id,omitempty"`
	Recipients    []UseRecipient `json:"recipients"`
	ReleaseScope  string         `json:"release_scope,omitempty"`
	Authenticator string         `json:"authenticator"`
	PersonID      string         `json:"person_id"`
	AttestedAt    string         `json:"attested_at"`
}

const maxAttestationHistory = 200

// Attestations returns a capability's presence-verified reveals and releases,
// newest first.
func (s *Service) Attestations(ctx context.Context, projectID, reference string, limit int) ([]Attestation, error) {
	row, err := s.projectRow(ctx, projectID, reference)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxAttestationHistory {
		limit = maxAttestationHistory
	}
	rows, err := s.queries.ListManagedSecretAttestations(ctx, db.ListManagedSecretAttestationsParams{
		SecretID: row.ID, LimitCount: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Attestation, 0, len(rows))
	for _, item := range rows {
		attestation := Attestation{
			AttestationID: item.AttestationID, Purpose: item.Purpose, Version: item.Version,
			CheckpointID: item.CheckpointID.String, ReleaseScope: item.ReleaseScope.String,
			Authenticator: item.Authenticator, PersonID: item.PersonID, AttestedAt: item.AttestedAt,
			Recipients: decodeUseRecipients(item.RecipientsJson),
		}
		out = append(out, attestation)
	}
	return out, nil
}
