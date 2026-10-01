package hitl

import "strings"

// DirectIPLease identifies reusable direct-IP authority.
type DirectIPLease struct {
	// ActionDigest is GrantKey(action): tool, project, canonical args, and boundary.
	ActionDigest string
	// RequestDigest covers the declared destinations, so a widened declaration re-asks.
	RequestDigest string
	// ConfinementDigest is attached roots, egress mode, and direct-IP narrowing.
	ConfinementDigest string
	// ChatConfinementDigest is attached roots and egress mode; a chat lease matches on it.
	ChatConfinementDigest string
	// DeclaredDestinations and CommandSummary are display facts.
	DeclaredDestinations []string
	CommandSummary       string
}

// Complete reports whether every identity field is present.
func (l DirectIPLease) Complete() bool {
	return strings.TrimSpace(l.ActionDigest) != "" &&
		strings.TrimSpace(l.RequestDigest) != "" &&
		strings.TrimSpace(l.ConfinementDigest) != ""
}

// IdentityKey joins the exact-action identity fields.
func (l DirectIPLease) IdentityKey() string {
	if !l.Complete() {
		return ""
	}
	return strings.TrimSpace(l.ActionDigest) + "\x00" +
		strings.TrimSpace(l.RequestDigest) + "\x00" +
		strings.TrimSpace(l.ConfinementDigest)
}

// DirectIPChatConfinementDigest joins the attached roots digest and egress mode.
func DirectIPChatConfinementDigest(roots []string, egress string) string {
	return RootsDigest(roots) + ":" + strings.TrimSpace(egress)
}

