package hitl

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/presence"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// ErrPresenceRequired refuses an approving answer to a plan that releases
// person-held values without the person's verified presence.
var ErrPresenceRequired = errors.New("releasing these values requires the person's presence")

// HeldSecret is one person-held value an approval would release.
type HeldSecret struct {
	SecretID string `json:"secret_id"`
	Version  int64  `json:"version"`
	Name     string `json:"name"`
}

// HeldRelease names the person-held values an approval would release and the
// recipients it would release them to. Every option that sends them needs
// the person's presence; nothing else may answer for them.
type HeldRelease struct {
	Secrets []HeldSecret `json:"secrets"`
	// Fingerprints are host-only identities of the held values' bytes.
	Fingerprints []string                `json:"fingerprints"`
	Recipients   []secretmatch.Recipient `json:"recipients"`
}

func (h *HeldRelease) empty() bool { return h == nil || len(h.Secrets) == 0 }

// merged combines two releases onto one card.
func (h *HeldRelease) merged(other *HeldRelease) *HeldRelease {
	if h.empty() {
		return other
	}
	if other.empty() {
		return h
	}
	out := &HeldRelease{
		Secrets:      append(append([]HeldSecret(nil), h.Secrets...), other.Secrets...),
		Fingerprints: append(append([]string(nil), h.Fingerprints...), other.Fingerprints...),
	}
	recipients, err := secretmatch.CanonicalRecipients(append(append([]secretmatch.Recipient(nil), h.Recipients...), other.Recipients...))
	if err == nil {
		out.Recipients = recipients
	}
	out.normalize()
	return out
}

func (h *HeldRelease) normalize() {
	sort.Slice(h.Secrets, func(i, j int) bool { return h.Secrets[i].SecretID < h.Secrets[j].SecretID })
	h.Secrets = slices.CompactFunc(h.Secrets, func(a, b HeldSecret) bool { return a.SecretID == b.SecretID })
	slices.Sort(h.Fingerprints)
	h.Fingerprints = slices.Compact(h.Fingerprints)
}

// Names lists the held values for presentation.
func (h *HeldRelease) Names() []string {
	if h == nil {
		return nil
	}
	names := make([]string, 0, len(h.Secrets))
	for _, secret := range h.Secrets {
		names = append(names, secret.Name)
	}
	return names
}

// holdsFingerprint reports whether fingerprint names a held value.
func (h *HeldRelease) holdsFingerprint(fingerprint string) bool {
	return h != nil && slices.Contains(h.Fingerprints, fingerprint)
}

// RequirePresence marks plan as releasing held values. Quiet choices and the
// day rung leave the ladder: a quiet would answer without the person, and a
// release lasts once, for the chat, or for the project. The returned plan has
// a new identity, so a presence challenge binds exactly this ladder.
func (p *ApprovalPlan) RequirePresence(held *HeldRelease) (*ApprovalPlan, error) {
	if p == nil || held.empty() {
		return p, nil
	}
	candidate := *p
	normalized := *held
	normalized.Secrets = slices.Clone(held.Secrets)
	normalized.Fingerprints = slices.Clone(held.Fingerprints)
	normalized.normalize()
	candidate.Held = p.Held.merged(&normalized)
	candidate.Options = nil
	for _, option := range p.Options {
		if option.Kind == ApprovalOptionQuiet || option.Rung == ApprovalRungDay ||
			slices.ContainsFunc(option.Authority, func(delta ApprovalAuthorityDelta) bool { return delta.Kind == AuthorityAskQuiet }) {
			continue
		}
		candidate.Options = append(candidate.Options, option)
	}
	if _, ok := candidate.Option(candidate.RecommendedOptionID); !ok {
		face, err := computeRecommendedOptionID(candidate.Subject.Kind, candidate.Reasons, candidate.Options, FaceContext{SecretManaged: true})
		if err != nil {
			return nil, err
		}
		candidate.RecommendedOptionID = face
	}
	id, err := candidate.canonicalID()
	if err != nil {
		return nil, err
	}
	candidate.ID = id
	stored, err := json.Marshal(candidate)
	if err != nil {
		return nil, fmt.Errorf("copy approval plan: %w", err)
	}
	var plan ApprovalPlan
	if err := json.Unmarshal(stored, &plan); err != nil {
		return nil, fmt.Errorf("copy approval plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return &plan, nil
}

// releasesHeld reports whether choosing option sends the plan's held values.
// A redacted or tracked send strips them, so only an approving option does.
func (p ApprovalPlan) releasesHeld(option ApprovalOption) bool {
	return !p.Held.empty() && option.DecisionAction == ApprovalOptionApprove
}

// validateHeld enforces the ladder RequirePresence produces.
func (p ApprovalPlan) validateHeld() error {
	if p.Held.empty() {
		return nil
	}
	for _, option := range p.Options {
		if option.Kind == ApprovalOptionQuiet || option.Rung == ApprovalRungDay {
			return fmt.Errorf("approval plan releasing held values offers %s", option.Rung)
		}
	}
	return nil
}

// ReleaseSubject is what a release presence challenge signs. The Rust shell
// reads checkpoint_id and option_id back before asking the person.
type ReleaseSubject struct {
	SessionID    string       `json:"session_id"`
	CheckpointID string       `json:"checkpoint_id"`
	OptionID     string       `json:"option_id"`
	PlanID       string       `json:"plan_id"`
	Secrets      []HeldSecret `json:"secrets"`
	RecipientSet string       `json:"recipient_set"`
}

// ReleaseRecorder writes the audit of an attested release inside the
// approval's commit.
type ReleaseRecorder interface {
	RecordReleaseTx(ctx context.Context, tx *sql.Tx, release AttestedRelease) error
}

// AttestedRelease is one presence-verified release, for audit.
type AttestedRelease struct {
	Attestation  presence.Attestation
	WindowLabel  string
	CheckpointID string
	// Scope is once, chat, or project.
	Scope      string
	Held       HeldRelease
	Recipients []secretmatch.Recipient
}

// releaseScope names how long option keeps releasing.
func releaseScope(option ApprovalOption) string {
	switch option.Rung {
	case ApprovalRungChat, ApprovalRungProject:
		return string(option.Rung)
	default:
		return "once"
	}
}

// heldReleasePrompt is the host-authored reason the operating system shows.
func heldReleasePrompt(held *HeldRelease, option ApprovalOption) string {
	names := presence.PromptText(strings.Join(held.Names(), ", "))
	labels := make([]string, 0, len(held.Recipients))
	for _, recipient := range held.Recipients {
		labels = append(labels, recipient.Label)
	}
	to := presence.PromptText(strings.Join(labels, ", "))
	how := map[string]string{
		"once":    "this once",
		"chat":    "for this chat",
		"project": "for this project",
	}[releaseScope(option)]
	return fmt.Sprintf("Use %s with %s %s in Painted Wolf Code.", names, to, how)
}

// attestHeldGrants fixes each held release grant's lifetime, lists it in the
// vault ledger, and stamps it with the attestation. The ledger entry is what
// lets a later send reuse the grant.
func attestHeldGrants(option ApprovalOption, held *HeldRelease, attestation presence.Attestation, ledger *presence.ReleaseLedger, now time.Time) (ApprovalOption, error) {
	authority := make([]ApprovalAuthorityDelta, len(option.Authority))
	for i, delta := range option.Authority {
		authority[i] = delta
		if delta.Grant == nil || delta.Grant.Predicate.Category != ApprovalGrantCategorySecret ||
			!slices.ContainsFunc(delta.Grant.SecretFingerprints, held.holdsFingerprint) {
			continue
		}
		grant := *delta.Grant
		grant.ExpiresAt = grant.ResolveExpiry(now)
		grant.TTLSeconds = 0
		stamped := attestation
		grant.Attestation = &stamped
		if err := ledger.Record(grant.ReleaseCoverage(), stamped); err != nil {
			return ApprovalOption{}, fmt.Errorf("record attested release: %w", err)
		}
		authority[i].Grant = &grant
	}
	option.Authority = authority
	return option, nil
}

// ReleaseCoverage is the exact authority an attested secret grant carries.
func (g ApprovalGrant) ReleaseCoverage() presence.ReleaseCoverage {
	return presence.ReleaseCoverage{
		GrantID: g.ID, Scope: string(g.Scope), ChatSessionID: g.ChatSessionID, ProjectID: g.ProjectID,
		ExpiresAt: g.ExpiresAt, Fingerprints: slices.Clone(g.SecretFingerprints),
		RecipientDigest: secretmatch.RecipientDigest(g.SecretRecipients),
	}
}
