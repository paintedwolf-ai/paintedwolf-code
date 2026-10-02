package settings

import (
	"context"

	"github.com/lycaon/lycaon/internal/hitl"
)

// BypassApprovalGate asks nothing. The same switch drops confinement
// (confine.BypassEnabled); the path resolver still refuses the control plane.
type BypassApprovalGate struct{}

// NewBypassApprovalGate returns a gate that never asks.
func NewBypassApprovalGate() hitl.ApprovalGate { return BypassApprovalGate{} }

// Evaluate asks nothing.
func (BypassApprovalGate) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	return &hitl.ApprovalResult{}, nil
}

func (BypassApprovalGate) GrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}

func (BypassApprovalGate) AbsorbedGrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}

func (BypassApprovalGate) ApplyGrant(hitl.ApprovalGrant) (bool, error) { return false, nil }

func (BypassApprovalGate) GrantCovers(hitl.ProposedAction) bool { return false }

func (BypassApprovalGate) HostResourceLeaseCovers(hitl.ProposedAction) bool { return false }

func (BypassApprovalGate) RevokeGrant(string) (bool, error) { return false, nil }

func (BypassApprovalGate) RevokeGrantInstalledBy(string, string) (bool, error) { return false, nil }

func (BypassApprovalGate) ListGrants(string) []hitl.ApprovalGrant { return nil }

func (BypassApprovalGate) SecretReleaseCovered(string, string, string, string, []string, []string) (bool, map[string]string) {
	return false, nil
}

// Bypass carries no standing redaction choice.
func (BypassApprovalGate) SecretRedactionStanding(string, []string) bool { return false }

func (BypassApprovalGate) PutAskQuiet(hitl.AskQuiet, int) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}

func (BypassApprovalGate) AskQuietLive(string, string) (hitl.AskQuiet, bool) {
	return hitl.AskQuiet{}, false
}

func (BypassApprovalGate) NoteAskQuietSuppressed(string, string) {}

func (BypassApprovalGate) ListAskQuiets(string) []hitl.AskQuiet { return nil }

func (BypassApprovalGate) RevokeAskQuiet(string) bool { return false }

func (BypassApprovalGate) RevokeAskQuietInstalledBy(string, string) bool { return false }

// ForgetSession is a no-op under bypass.
func (BypassApprovalGate) ForgetSession(string) {}
