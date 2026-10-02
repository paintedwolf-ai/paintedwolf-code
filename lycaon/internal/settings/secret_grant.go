package settings

import (
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// SecretRedactionStanding reports complete standing-redaction coverage.
func (g *RuleApprovalGate) SecretRedactionStanding(projectID string, fingerprints []string) bool {
	return g.secretFingerprintsCoveredBy(ApprovalCategorySecretRedact, "", projectID, "", "", fingerprints)
}

// SecretFingerprintsCovered reports complete release coverage for one reviewed seam.
func (g *RuleApprovalGate) SecretFingerprintsCovered(chatSessionID, projectID, destinationID, surface string, fingerprints []string) bool {
	return g.secretFingerprintsCoveredBy(ApprovalCategorySecret, chatSessionID, projectID, destinationID, surface, fingerprints)
}

func (g *RuleApprovalGate) secretFingerprintsCoveredBy(
	category ApprovalCategory,
	chatSessionID, projectID, destinationID, surface string,
	fingerprints []string,
) bool {
	if g == nil || g.store == nil {
		return false
	}
	projectID = strings.TrimSpace(projectID)
	destinationID = strings.TrimSpace(destinationID)
	surface = strings.TrimSpace(surface)
	want := make(map[string]struct{}, len(fingerprints))
	for _, fingerprint := range fingerprints {
		if fingerprint == "" {
			return false
		}
		want[fingerprint] = struct{}{}
	}
	if projectID == "" || len(want) == 0 || (category == ApprovalCategorySecret && (destinationID == "" || surface == "")) {
		return false
	}
	covered := make(map[string]struct{}, len(want))
	if category == ApprovalCategorySecret && g.grants != nil {
		now := time.Now()
		for _, grant := range g.grants.live(strings.TrimSpace(chatSessionID)) {
			if grant.Predicate.Category != string(category) || strings.TrimSpace(grant.ProjectID) != projectID ||
				!secretmatch.RecipientCovered(grant.SecretRecipients, destinationID, surface) ||
				(category == ApprovalCategorySecret && !hitl.WitnessEqual(grant.Witness, hitl.SecretReleaseWitness(grant.SecretRecipients))) ||
				(grant.ExpiresAt != nil && !grant.ExpiresAt.After(now)) {
				continue
			}
			for _, fingerprint := range grant.SecretFingerprints {
				covered[fingerprint] = struct{}{}
			}
		}
	}
	for _, grant := range g.store.GlobalGrants() {
		if grant.Category != category {
			continue
		}
		// Device-scoped redactions apply across projects.
		if grant.Scope != hitl.ApprovalGrantScopeDevice && strings.TrimSpace(grant.ProjectID) != projectID {
			continue
		}
		if category == ApprovalCategorySecret && (!secretmatch.RecipientCovered(grant.SecretRecipients, destinationID, surface)) {
			continue
		}
		if category == ApprovalCategorySecret && !hitl.WitnessEqual(grant.Witness, hitl.SecretReleaseWitness(grant.SecretRecipients)) {
			continue
		}
		for _, fingerprint := range grant.SecretFingerprints {
			covered[fingerprint] = struct{}{}
		}
	}
	for fingerprint := range want {
		if _, ok := covered[fingerprint]; !ok {
			return false
		}
	}
	return true
}
