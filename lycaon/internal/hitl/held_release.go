package hitl

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrPresenceRequired refuses an approving answer that would send person-held
// values while their chat is locked and no verified presence came with it.
var ErrPresenceRequired = errors.New("using these values requires the person's presence")

// unlockOptionID is the unlock card's one approving choice.
const unlockOptionID = "unlock_for_chat"

// Copy for the card that only unlocks a chat.
const (
	TitleUnlockForThisChat = "Unlock for this chat"
	unlockSubjectTitle     = "Unlock values you stored"
	unlockImpact           = "The recipients are already approved. Unlocking lets this chat use these values until 15 minutes pass unused, 4 hours pass, or this computer locks or sleeps."
)

// HeldSecret is one person-held value an approval would send.
type HeldSecret struct {
	SecretID string `json:"secret_id"`
	Version  int64  `json:"version"`
	Name     string `json:"name"`
}

// HeldRelease names the person-held values an approval would send, the
// recipients, and the chat whose unlock they leave under. Approving
// is an ordinary choice while that chat is unlocked; while it is locked the
// approval needs the person's verified presence, which also opens it.
type HeldRelease struct {
	ProjectID     string                  `json:"project_id"`
	ChatSessionID string                  `json:"chat_session_id"`
	Secrets       []HeldSecret            `json:"secrets"`
	Recipients    []secretmatch.Recipient `json:"recipients"`
	// UnlockOnly marks a card whose recipients are already approved: it
	// exists to unlock the chat.
	UnlockOnly bool `json:"unlock_only,omitempty"`
}

func (h *HeldRelease) empty() bool { return h == nil || len(h.Secrets) == 0 }

// merged combines two releases from one chat onto one card.
func (h *HeldRelease) merged(other *HeldRelease) *HeldRelease {
	if h.empty() {
		return other
	}
	if other.empty() {
		return h
	}
	out := &HeldRelease{
		ProjectID: h.ProjectID, ChatSessionID: h.ChatSessionID,
		Secrets: append(append([]HeldSecret(nil), h.Secrets...), other.Secrets...),
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

// WithHeld marks plan as sending person-held values. Quiet choices leave the
// ladder, because a quiet would send them to recipients nobody reviewed; the
// rest of the ladder is the ordinary one. The returned plan has a new
// identity, so a presence challenge binds exactly this card.
func (p *ApprovalPlan) WithHeld(held *HeldRelease) (*ApprovalPlan, error) {
	if p == nil || held.empty() {
		return p, nil
	}
	if p.Held != nil && held.UnlockOnly != p.Held.UnlockOnly {
		return nil, fmt.Errorf("an unlock card cannot also approve recipients")
	}
	candidate := *p
	normalized := *held
	normalized.Secrets = slices.Clone(held.Secrets)
	normalized.normalize()
	candidate.Held = p.Held.merged(&normalized)
	candidate.Held.UnlockOnly = held.UnlockOnly
	candidate.Options = nil
	for _, option := range p.Options {
		if option.Kind == ApprovalOptionQuiet ||
			slices.ContainsFunc(option.Authority, func(delta ApprovalAuthorityDelta) bool { return delta.Kind == AuthorityAskQuiet }) {
			continue
		}
		candidate.Options = append(candidate.Options, option)
	}
	if err := candidate.refaceAfterFilter(FaceContext{SecretManaged: true}); err != nil {
		return nil, err
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

// NewUnlockPlan is the card that unlocks a chat for values whose
// recipients are already approved. Unlocking needs presence; the redacted
// send the screen allows stays available and leaves the chat locked.
func NewUnlockPlan(action ProposedAction, held HeldRelease, screen *SecretScreen) (*ApprovalPlan, error) {
	held.UnlockOnly = true
	subject := ApprovalSubject{
		Kind: ApprovalSubjectSecret, Title: unlockSubjectTitle,
		Targets: []ApprovalTarget{{
			Kind: "secret", Label: strings.Join(held.Names(), ", "),
			Details: map[string]any{secretGenericShapeDetail: "Protected values"},
		}},
	}
	presentation := ApprovalPresentation{
		Action: TitleUnlockForThisChat, Tool: action.Tool, Impact: unlockImpact,
		Gate: api.GateSecretOutbound, ConsequenceBand: string(api.ConsequenceBandHighRisk),
		ConsequenceCode: string(api.ConsequenceCodeSecret),
		Cited: []PresentedFact{{Gate: api.GateSecretOutbound, Key: "secret.custody", Value: "a value you stored", Source: "managed_secret"}},
	}
	unlock := SendUnchangedOption()
	unlock.ID, unlock.Title = unlockOptionID, TitleUnlockForThisChat
	unlock.Coverage = "uses of these values with recipients this chat already approved"
	unlock.ExpiresWhen = "after 15 minutes without use, after 4 hours, or when this computer locks or sleeps"
	unlock.ReaskWhen = "the unlock ends"
	redacted := secretRedactedOption(screen)
	if redacted.Disabled {
		presentation.OptionNote = redacted.Note
	}
	plan, err := NewApprovalPlan(action, ApprovalStagePreSend, subject, presentation,
		[]api.ApprovalGate{api.GateSecretOutbound}, []ApprovalOption{redacted, unlock}, FaceContext{SecretManaged: true})
	if err != nil {
		return nil, err
	}
	return plan.WithHeld(&held)
}

// releasesHeld reports whether choosing option sends the plan's held values.
// A redacted or tracked send strips them, so only an approving option does.
func (p ApprovalPlan) releasesHeld(option ApprovalOption) bool {
	return !p.Held.empty() && option.DecisionAction == ApprovalOptionApprove
}

// validateHeld enforces the ladder WithHeld produces.
func (p ApprovalPlan) validateHeld() error {
	if p.Held.empty() {
		return nil
	}
	if strings.TrimSpace(p.Held.ChatSessionID) == "" || strings.TrimSpace(p.Held.ProjectID) == "" {
		return fmt.Errorf("approval plan sending held values names no chat")
	}
	for _, option := range p.Options {
		if option.Kind == ApprovalOptionQuiet {
			return fmt.Errorf("approval plan sending held values offers a quiet")
		}
	}
	return nil
}
