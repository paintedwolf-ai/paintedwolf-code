// Package presence verifies that a person is at this device before the host
// discloses a value they hold.
//
// The desktop shell owns an ephemeral Ed25519 key for its launch and signs a
// challenge only after the operating system confirms user presence. The API
// bearer can begin a challenge but cannot complete one. Reveal and release
// share this broker; each challenge names its purpose inside the signed
// payload, so a proof for one purpose never verifies for the other.
package presence

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	// PublicKeyEnv carries the native shell's ephemeral verification key.
	PublicKeyEnv = "LYCAON_PRESENCE_PUBLIC_KEY"

	AuthenticatorMacOS   = "macos_user_presence"
	AuthenticatorWindows = "windows_user_presence"

	protocol = "painted-wolf-presence-v1"

	// PromptTimeout bounds the operating-system presence prompt.
	PromptTimeout = 120 * time.Second
	// challengeLifetime includes prompt time and transport margin.
	challengeLifetime = PromptTimeout + 30*time.Second

	maxPending          = 32
	maxWindowLabelRunes = 120
)

// Purpose names what a verified presence authorizes.
type Purpose string

const (
	// PurposeReveal shows a held value in the person's own view.
	PurposeReveal Purpose = "reveal"
	// PurposeRelease hands held values to the recipients one approval names.
	PurposeRelease Purpose = "release"
)

var (
	ErrUnavailable = errors.New("presence verification is unavailable on this device")
	ErrDenied      = errors.New("presence verification was denied")
	// ErrChallengeNotFound: the challenge was never issued, expired, or was
	// already used.
	ErrChallengeNotFound = errors.New("presence challenge not found")
)

// Claim is what one challenge binds before the person is asked.
type Claim struct {
	Purpose     Purpose
	PersonID    string
	WindowLabel string
	// Key replaces an earlier pending challenge for the same subject and window.
	Key string
	// Subject is the purpose-specific binding, signed inside the payload.
	Subject any
}

// Challenge is a value-free, single-use presence request.
type Challenge struct {
	ID           string
	ProofPayload string
	ExpiresAt    string
}

// Proof is the native shell's signed answer to one challenge.
type Proof struct {
	ChallengeID   string
	Authenticator string
	Signature     string
}

// Verified is a consumed challenge whose signature the shell's key produced.
type Verified struct {
	ChallengeID   string
	Purpose       Purpose
	PersonID      string
	WindowLabel   string
	Authenticator string
	Subject       json.RawMessage
	VerifiedAt    time.Time
}

// payload is the exact document the shell signs.
type payload struct {
	Protocol    string          `json:"protocol"`
	Purpose     Purpose         `json:"purpose"`
	ChallengeID string          `json:"challenge_id"`
	Nonce       string          `json:"nonce"`
	WindowLabel string          `json:"window_label"`
	ExpiresAt   string          `json:"expires_at"`
	Subject     json.RawMessage `json:"subject"`
}

type pending struct {
	payload  payload
	encoded  string
	key      string
	personID string
	deadline time.Time
}

// Broker issues and consumes presence challenges in memory.
type Broker struct {
	mu         sync.Mutex
	now        func() time.Time
	publicKey  ed25519.PublicKey
	challenges map[string]pending
}

// NewBroker returns a broker with no verification key installed.
func NewBroker() *Broker {
	return &Broker{now: time.Now, challenges: make(map[string]pending)}
}

// SetClock replaces the broker's clock in tests.
func (b *Broker) SetClock(now func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = now
}

// Configure installs the shell's verification key. An empty key leaves
// presence unavailable; installing a key discards pending challenges.
func (b *Broker) Configure(encoded string) error {
	encoded = strings.TrimSpace(encoded)
	b.mu.Lock()
	defer b.mu.Unlock()
	clear(b.challenges)
	if encoded == "" {
		b.publicKey = nil
		return nil
	}
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize {
		b.publicKey = nil
		return fmt.Errorf("%w: the native verification key is invalid", ErrUnavailable)
	}
	b.publicKey = append(ed25519.PublicKey(nil), key...)
	return nil
}

// Available reports whether a verification key is installed.
func (b *Broker) Available() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.publicKey) == ed25519.PublicKeySize
}

// Begin issues one challenge for claim.
func (b *Broker) Begin(claim Claim) (Challenge, error) {
	if b == nil {
		return Challenge{}, ErrUnavailable
	}
	if claim.Purpose != PurposeReveal && claim.Purpose != PurposeRelease {
		return Challenge{}, fmt.Errorf("%w: unknown purpose %q", ErrDenied, claim.Purpose)
	}
	personID := strings.TrimSpace(claim.PersonID)
	if personID == "" {
		return Challenge{}, fmt.Errorf("%w: the person is required", ErrDenied)
	}
	windowLabel := strings.TrimSpace(claim.WindowLabel)
	if !IsWindowLabel(windowLabel) {
		return Challenge{}, fmt.Errorf("%w: invalid native window label", ErrDenied)
	}
	subject, err := json.Marshal(claim.Subject)
	if err != nil {
		return Challenge{}, fmt.Errorf("encode presence subject: %w", err)
	}
	nonce, err := randomNonce()
	if err != nil {
		return Challenge{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.publicKey) != ed25519.PublicKeySize {
		return Challenge{}, ErrUnavailable
	}
	now := b.now().UTC()
	b.prune(now)
	deadline := now.Add(challengeLifetime)
	doc := payload{
		Protocol: protocol, Purpose: claim.Purpose, ChallengeID: uuid.NewString(), Nonce: nonce,
		WindowLabel: windowLabel, ExpiresAt: deadline.Format(time.RFC3339Nano), Subject: subject,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return Challenge{}, fmt.Errorf("encode presence payload: %w", err)
	}
	key := string(claim.Purpose) + "\x00" + claim.Key + "\x00" + windowLabel
	// Replace a caller's challenge before measuring capacity.
	for id, existing := range b.challenges {
		if existing.key == key {
			delete(b.challenges, id)
		}
	}
	// A full pool preserves issued challenges.
	if len(b.challenges) >= maxPending {
		return Challenge{}, fmt.Errorf("%w: too many presence challenges are pending", ErrDenied)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	b.challenges[doc.ChallengeID] = pending{
		payload: doc, encoded: encoded, key: key, personID: personID, deadline: deadline,
	}
	return Challenge{ID: doc.ChallengeID, ProofPayload: encoded, ExpiresAt: doc.ExpiresAt}, nil
}

// Complete consumes the challenge on its first attempt and verifies that the
// shell signed it for purpose on behalf of the person who began it.
func (b *Broker) Complete(proof Proof, purpose Purpose, personID string) (Verified, error) {
	if b == nil {
		return Verified{}, ErrUnavailable
	}
	authenticator := strings.TrimSpace(proof.Authenticator)
	if !IsAuthenticator(authenticator) {
		return Verified{}, fmt.Errorf("%w: unknown authenticator", ErrDenied)
	}
	b.mu.Lock()
	now := b.now().UTC()
	b.prune(now)
	issued, ok := b.challenges[strings.TrimSpace(proof.ChallengeID)]
	if ok {
		delete(b.challenges, issued.payload.ChallengeID)
	}
	key := append(ed25519.PublicKey(nil), b.publicKey...)
	b.mu.Unlock()
	if !ok {
		return Verified{}, ErrChallengeNotFound
	}
	if len(key) != ed25519.PublicKeySize {
		return Verified{}, ErrUnavailable
	}
	if issued.payload.Purpose != purpose || issued.personID != strings.TrimSpace(personID) {
		return Verified{}, ErrDenied
	}
	signature, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(proof.Signature))
	if err != nil || len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(key, SigningMessage(issued.encoded, authenticator), signature) {
		return Verified{}, ErrDenied
	}
	return Verified{
		ChallengeID: issued.payload.ChallengeID, Purpose: issued.payload.Purpose, PersonID: issued.personID,
		WindowLabel: issued.payload.WindowLabel, Authenticator: authenticator,
		Subject: issued.payload.Subject, VerifiedAt: now,
	}, nil
}

func (b *Broker) prune(now time.Time) {
	for id, issued := range b.challenges {
		if !now.Before(issued.deadline) {
			delete(b.challenges, id)
		}
	}
}

// SigningMessage is the exact byte string the shell signs.
func SigningMessage(proofPayload, authenticator string) []byte {
	return []byte(protocol + "\n" + proofPayload + "\nauthenticator=" + authenticator)
}

func randomNonce() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create presence nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}

// IsAuthenticator reports whether value names a supported native
// user-presence authenticator.
func IsAuthenticator(value string) bool {
	return value == AuthenticatorMacOS || value == AuthenticatorWindows
}

// IsWindowLabel reports whether value is a usable native window label.
func IsWindowLabel(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxWindowLabelRunes {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

// PromptText flattens host-authored prompt fragments to one line.
func PromptText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
