package secretcap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/db"
)

const (
	// RevealPublicKeyEnv carries the native shell's ephemeral public key.
	RevealPublicKeyEnv = "LYCAON_SECRET_REVEAL_PUBLIC_KEY"

	RevealAuthenticatorMacOS   = "macos_user_presence"
	RevealAuthenticatorWindows = "windows_user_presence"

	revealProtocol = "painted-wolf-managed-secret-reveal-v1"

	// nativePromptTimeout bounds the operating-system presence prompt.
	nativePromptTimeout = 120 * time.Second
	// revealChallengeLifetime includes prompt time and transport margin.
	revealChallengeLifetime = nativePromptTimeout + 30*time.Second

	revealRemaskAfter    = 30 * time.Second
	maxPendingReveals    = 32
	maxRevealWindowRunes = 120
)

// RevealChallenge is a value-free, single-use authentication challenge.
type RevealChallenge struct {
	ID           string
	ProofPayload string
	Prompt       string
	Version      int64
	ExpiresAt    string
}

// RevealResult carries an authenticated disclosure.
type RevealResult struct {
	Value              credentialstore.SecretValue
	Version            int64
	RevealedAt         string
	RemaskAfterSeconds int64
}

type revealProof struct {
	Protocol    string `json:"protocol"`
	ChallengeID string `json:"challenge_id"`
	Nonce       string `json:"nonce"`
	ProjectID   string `json:"project_id"`
	SecretID    string `json:"secret_id"`
	Version     int64  `json:"version"`
	WindowLabel string `json:"window_label"`
	ExpiresAt   string `json:"expires_at"`
}

type pendingReveal struct {
	proof        revealProof
	proofPayload string
	// personID is who asked; only they may complete the challenge.
	personID string
}

type revealBroker struct {
	mu         sync.Mutex
	publicKey  ed25519.PublicKey
	challenges map[string]pendingReveal
}

func newRevealBroker() *revealBroker {
	return &revealBroker{challenges: make(map[string]pendingReveal)}
}

// ConfigureRevealPublicKey installs the native shell's verification key.
func (s *Service) ConfigureRevealPublicKey(encoded string) error {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil
	}
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: native reveal public key is invalid", ErrRevealUnavailable)
	}
	if s.reveal == nil {
		s.reveal = newRevealBroker()
	}
	s.reveal.mu.Lock()
	defer s.reveal.mu.Unlock()
	s.reveal.publicKey = append(ed25519.PublicKey(nil), key...)
	clear(s.reveal.challenges)
	return nil
}

// RevealAvailable reports whether native proof is configured.
func (s *Service) RevealAvailable() bool {
	if s == nil || s.reveal == nil {
		return false
	}
	s.reveal.mu.Lock()
	defer s.reveal.mu.Unlock()
	return len(s.reveal.publicKey) == ed25519.PublicKeySize
}

// BeginReveal binds authentication to one value version, window, and person.
func (s *Service) BeginReveal(
	ctx context.Context, projectID, reference, windowLabel, personID string,
) (RevealChallenge, error) {
	row, err := s.projectRow(ctx, projectID, reference)
	if err != nil {
		return RevealChallenge{}, err
	}
	if !s.RevealAvailable() {
		return RevealChallenge{}, ErrRevealUnavailable
	}
	personID = strings.TrimSpace(personID)
	if personID == "" {
		return RevealChallenge{}, fmt.Errorf("%w: the revealing person is required", ErrRevealDenied)
	}
	windowLabel = strings.TrimSpace(windowLabel)
	if !IsRevealWindowLabel(windowLabel) {
		return RevealChallenge{}, fmt.Errorf("%w: invalid native window label", ErrRevealDenied)
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
	if _, ok := s.values.Get(current.ID); !ok {
		return RevealChallenge{}, ErrValueMissing
	}

	now := s.now().UTC()
	nonce, err := randomRevealNonce()
	if err != nil {
		return RevealChallenge{}, err
	}
	proof := revealProof{
		Protocol: revealProtocol, ChallengeID: uuid.NewString(), Nonce: nonce,
		ProjectID: row.ProjectID, SecretID: row.ID, Version: current.Version,
		WindowLabel: windowLabel, ExpiresAt: now.Add(revealChallengeLifetime).Format(time.RFC3339Nano),
	}
	payload, err := json.Marshal(proof)
	if err != nil {
		return RevealChallenge{}, fmt.Errorf("encode managed secret reveal proof: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	if err := s.reveal.put(now, pendingReveal{proof: proof, proofPayload: encoded, personID: personID}); err != nil {
		return RevealChallenge{}, err
	}
	return RevealChallenge{
		ID: proof.ChallengeID, ProofPayload: encoded,
		Prompt: revealPrompt(row.Name, row.Purpose), Version: current.Version,
		ExpiresAt: proof.ExpiresAt,
	}, nil
}

// CompleteReveal consumes one challenge and records disclosure to the person
// who began it before return.
func (s *Service) CompleteReveal(
	ctx context.Context, projectID, reference, challengeID, personID, authenticator, signature string,
) (RevealResult, error) {
	row, err := s.projectRow(ctx, projectID, reference)
	if err != nil {
		return RevealResult{}, err
	}
	authenticator = strings.TrimSpace(authenticator)
	if !IsRevealAuthenticator(authenticator) {
		return RevealResult{}, ErrRevealDenied
	}
	if s.reveal == nil {
		return RevealResult{}, ErrRevealUnavailable
	}
	// Challenges exist only while a verifier key is configured, so an unknown
	// challenge answers before availability.
	pending, key, err := s.reveal.take(strings.TrimSpace(challengeID), s.now().UTC())
	if err != nil {
		return RevealResult{}, err
	}
	if len(key) != ed25519.PublicKeySize {
		return RevealResult{}, ErrRevealUnavailable
	}
	if pending.proof.ProjectID != strings.TrimSpace(projectID) || pending.personID != strings.TrimSpace(personID) {
		return RevealResult{}, ErrRevealDenied
	}
	if pending.proof.SecretID != row.ID {
		return RevealResult{}, ErrRevealDenied
	}
	decodedSignature, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(decodedSignature) != ed25519.SignatureSize ||
		!ed25519.Verify(key, revealSigningMessage(pending.proofPayload, authenticator), decodedSignature) {
		return RevealResult{}, ErrRevealDenied
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
	if current.ID == "" || current.Version != pending.proof.Version {
		return RevealResult{}, ErrRevealChanged
	}
	value, ok := s.values.Get(current.ID)
	if !ok {
		return RevealResult{}, ErrValueMissing
	}
	revealedAt := db.FormatTime(s.now())
	if err := s.queries.CreateManagedSecretReveal(ctx, db.CreateManagedSecretRevealParams{
		ID: uuid.NewString(), SecretID: row.ID, Version: current.Version,
		Authenticator: authenticator, WindowLabel: pending.proof.WindowLabel, PersonID: pending.personID,
		RevealedAt: revealedAt,
	}); err != nil {
		return RevealResult{}, fmt.Errorf("record managed secret reveal: %w", err)
	}
	return RevealResult{
		Value: value, Version: current.Version, RevealedAt: revealedAt,
		RemaskAfterSeconds: int64(revealRemaskAfter / time.Second),
	}, nil
}

func (b *revealBroker) put(now time.Time, pending pendingReveal) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(now)
	if len(b.publicKey) != ed25519.PublicKeySize {
		return ErrRevealUnavailable
	}
	// Replace a caller's challenge before measuring capacity.
	for id, existing := range b.challenges {
		if existing.proof.SecretID == pending.proof.SecretID &&
			existing.proof.WindowLabel == pending.proof.WindowLabel {
			delete(b.challenges, id)
		}
	}
	// A full pool preserves issued challenges.
	if len(b.challenges) >= maxPendingReveals {
		return fmt.Errorf("%w: too many reveal challenges are pending", ErrRevealDenied)
	}
	b.challenges[pending.proof.ChallengeID] = pending
	return nil
}

func (b *revealBroker) take(id string, now time.Time) (pendingReveal, ed25519.PublicKey, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(now)
	pending, ok := b.challenges[id]
	if !ok {
		return pendingReveal{}, nil, ErrRevealChallengeNotFound
	}
	delete(b.challenges, id)
	deadline, err := time.Parse(time.RFC3339Nano, pending.proof.ExpiresAt)
	if err != nil || !now.Before(deadline) {
		return pendingReveal{}, nil, ErrRevealDenied
	}
	return pending, append(ed25519.PublicKey(nil), b.publicKey...), nil
}

func (b *revealBroker) prune(now time.Time) {
	for id, pending := range b.challenges {
		deadline, err := time.Parse(time.RFC3339Nano, pending.proof.ExpiresAt)
		if err != nil || !now.Before(deadline) {
			delete(b.challenges, id)
		}
	}
}

func randomRevealNonce() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create managed secret reveal nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

func revealSigningMessage(proofPayload, authenticator string) []byte {
	return []byte(revealProtocol + "\n" + proofPayload + "\nauthenticator=" + authenticator)
}

// IsRevealAuthenticator reports whether value names a supported native
// user-presence authenticator.
func IsRevealAuthenticator(value string) bool {
	return value == RevealAuthenticatorMacOS || value == RevealAuthenticatorWindows
}

// IsRevealWindowLabel reports whether value is a usable native window label.
func IsRevealWindowLabel(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRevealWindowRunes {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func revealPrompt(name, purpose string) string {
	name = revealPromptText(name)
	purpose = revealPromptText(purpose)
	if purpose == "" {
		return fmt.Sprintf("Reveal the current value for %q in Painted Wolf Code.", name)
	}
	return fmt.Sprintf("Reveal the current value for %q in Painted Wolf Code. Purpose: %s", name, purpose)
}

func revealPromptText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
