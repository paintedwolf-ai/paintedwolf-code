package presence

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
)

const ledgerContext = "presence release ledger"

// Attestation is the verified presence a release grant carries, for audit.
// It is never authority by itself: a grant covers a held value only while
// the vault's release ledger lists it.
type Attestation struct {
	ID            string    `json:"id" yaml:"id"`
	PersonID      string    `json:"person_id" yaml:"person_id"`
	Authenticator string    `json:"authenticator" yaml:"authenticator"`
	AttestedAt    time.Time `json:"attested_at" yaml:"attested_at"`
}

// ReleaseCoverage is the exact authority one attested release grant carries.
type ReleaseCoverage struct {
	GrantID         string
	Scope           string
	ChatSessionID   string
	ProjectID       string
	ExpiresAt       *time.Time
	Fingerprints    []string
	RecipientDigest string
}

// ReleaseLedger lists the live release grants a person attested, inside the
// encrypted vault. Grants themselves live in ordinary storage that any
// same-user process can write; the ledger is what such a process cannot
// forge, extend, or restore after a person revokes the grant.
type ReleaseLedger struct {
	store *credentialstore.Store
	now   func() time.Time
}

// OpenReleaseLedger loads the ledger from the credential vault.
func OpenReleaseLedger() (*ReleaseLedger, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return nil, err
	}
	store, err := credentialstore.Open(credentialstore.Slot{
		Path: credentialstore.DefaultVaultPath(dir), Namespace: credentialstore.NamespacePresenceReleases,
		Context: ledgerContext,
	}, validGrantID)
	if err != nil {
		return nil, err
	}
	return NewReleaseLedger(store), nil
}

// NewReleaseLedger wraps an explicit vault namespace.
func NewReleaseLedger(store *credentialstore.Store) *ReleaseLedger {
	return &ReleaseLedger{store: store, now: time.Now}
}

func validGrantID(id string) bool {
	return strings.TrimSpace(id) != "" && len(id) <= 512
}

// ledgerEntry is one vault record: the coverage digest and its expiry.
type ledgerEntry struct {
	Digest    string     `json:"digest"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Record lists an attested grant. It runs before the grant is installed, so
// a failure leaves no authority behind.
func (l *ReleaseLedger) Record(coverage ReleaseCoverage, attestation Attestation) error {
	if l == nil {
		return ErrUnavailable
	}
	digest, err := coverageDigest(coverage, attestation)
	if err != nil {
		return err
	}
	l.prune()
	encoded, err := json.Marshal(ledgerEntry{Digest: digest, ExpiresAt: coverage.ExpiresAt})
	if err != nil {
		return fmt.Errorf("encode release ledger entry: %w", err)
	}
	return l.store.Set(coverage.GrantID, string(encoded))
}

// Covers reports whether the ledger lists exactly this attested grant and
// it has not expired.
func (l *ReleaseLedger) Covers(coverage ReleaseCoverage, attestation Attestation) bool {
	if l == nil || strings.TrimSpace(attestation.ID) == "" {
		return false
	}
	raw, ok := l.store.Get(coverage.GrantID)
	if !ok {
		return false
	}
	var entry ledgerEntry
	if err := json.Unmarshal([]byte(raw.Value()), &entry); err != nil {
		return false
	}
	if entry.ExpiresAt != nil && !l.now().Before(*entry.ExpiresAt) {
		return false
	}
	digest, err := coverageDigest(coverage, attestation)
	return err == nil && digest == entry.Digest
}

// Forget removes a revoked grant from the ledger.
func (l *ReleaseLedger) Forget(grantID string) error {
	if l == nil || !validGrantID(grantID) {
		return nil
	}
	if _, ok := l.store.Get(grantID); !ok {
		return nil
	}
	return l.store.Delete(grantID)
}

// prune drops expired entries; a failed delete only delays the cleanup.
func (l *ReleaseLedger) prune() {
	now := l.now()
	for _, id := range l.store.IDs() {
		raw, ok := l.store.Get(id)
		if !ok {
			continue
		}
		var entry ledgerEntry
		if err := json.Unmarshal([]byte(raw.Value()), &entry); err != nil ||
			(entry.ExpiresAt != nil && !now.Before(*entry.ExpiresAt)) {
			_ = l.store.Delete(id)
		}
	}
}

type coverageDocument struct {
	GrantID         string   `json:"grant_id"`
	Scope           string   `json:"scope"`
	ChatSessionID   string   `json:"chat_session_id"`
	ProjectID       string   `json:"project_id"`
	ExpiresAt       string   `json:"expires_at"`
	Fingerprints    []string `json:"fingerprints"`
	RecipientDigest string   `json:"recipient_digest"`
	AttestationID   string   `json:"attestation_id"`
	PersonID        string   `json:"person_id"`
	Authenticator   string   `json:"authenticator"`
	AttestedAt      string   `json:"attested_at"`
}

// coverageDigest is a canonical identity for one attested grant. Times are
// compared at second precision, the finest every grant store keeps.
func coverageDigest(coverage ReleaseCoverage, attestation Attestation) (string, error) {
	if strings.TrimSpace(coverage.GrantID) == "" || strings.TrimSpace(attestation.ID) == "" {
		return "", fmt.Errorf("%w: an attested release names its grant and attestation", ErrDenied)
	}
	fingerprints := slices.Clone(coverage.Fingerprints)
	slices.Sort(fingerprints)
	expires := ""
	if coverage.ExpiresAt != nil {
		expires = coverage.ExpiresAt.UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	doc, err := json.Marshal(coverageDocument{
		GrantID: coverage.GrantID, Scope: coverage.Scope, ChatSessionID: coverage.ChatSessionID,
		ProjectID: coverage.ProjectID, ExpiresAt: expires, Fingerprints: slices.Compact(fingerprints),
		RecipientDigest: coverage.RecipientDigest, AttestationID: attestation.ID,
		PersonID: attestation.PersonID, Authenticator: attestation.Authenticator,
		AttestedAt: attestation.AttestedAt.UTC().Truncate(time.Second).Format(time.RFC3339),
	})
	if err != nil {
		return "", fmt.Errorf("encode release coverage: %w", err)
	}
	sum := sha256.Sum256(doc)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}
