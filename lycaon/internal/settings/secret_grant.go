package settings

import (
	"slices"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

// SecretRedactionStanding reports complete standing-redaction coverage.
func (g *RuleApprovalGate) SecretRedactionStanding(projectID string, fingerprints []string) bool {
	covered, _ := g.secretFingerprintsCoveredBy(ApprovalCategorySecretRedact, "", projectID, "", "", fingerprints, nil)
	return covered
}

// SecretReleaseCovered reports complete release coverage for one reviewed
// seam. A person-held fingerprint is covered only by a grant the vault's
// release ledger lists; attestations names each held fingerprint's release.
func (g *RuleApprovalGate) SecretReleaseCovered(chatSessionID, projectID, destinationID, surface string, fingerprints, held []string) (bool, map[string]string) {
	return g.secretFingerprintsCoveredBy(ApprovalCategorySecret, chatSessionID, projectID, destinationID, surface, fingerprints, held)
}

// releaseCandidate is one live grant's secret coverage.
type releaseCandidate struct {
	fingerprints []string
	attested     bool
	attestation  string
}

func (g *RuleApprovalGate) secretFingerprintsCoveredBy(
	category ApprovalCategory,
	chatSessionID, projectID, destinationID, surface string,
	fingerprints, held []string,
) (bool, map[string]string) {
	if g == nil || g.store == nil {
		return false, nil
	}
	projectID = strings.TrimSpace(projectID)
	destinationID = strings.TrimSpace(destinationID)
	surface = strings.TrimSpace(surface)
	want := make(map[string]struct{}, len(fingerprints))
	for _, fingerprint := range fingerprints {
		if fingerprint == "" {
			return false, nil
		}
		want[fingerprint] = struct{}{}
	}
	if projectID == "" || len(want) == 0 || (category == ApprovalCategorySecret && (destinationID == "" || surface == "")) {
		return false, nil
	}
	var candidates []releaseCandidate
	if category == ApprovalCategorySecret && g.grants != nil {
		now := time.Now()
		for _, grant := range g.grants.live(strings.TrimSpace(chatSessionID)) {
			if grant.Predicate.Category != string(category) || strings.TrimSpace(grant.ProjectID) != projectID ||
				!secretmatch.RecipientCovered(grant.SecretRecipients, destinationID, surface) ||
				!hitl.WitnessEqual(grant.Witness, hitl.SecretReleaseWitness(grant.SecretRecipients)) ||
				(grant.ExpiresAt != nil && !grant.ExpiresAt.After(now)) {
				continue
			}
			candidates = append(candidates, g.releaseCandidate(grant))
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
		if category == ApprovalCategorySecret && (!secretmatch.RecipientCovered(grant.SecretRecipients, destinationID, surface) ||
			!hitl.WitnessEqual(grant.Witness, hitl.SecretReleaseWitness(grant.SecretRecipients))) {
			continue
		}
		candidates = append(candidates, g.releaseCandidate(grant.ToDomain()))
	}
	covered := make(map[string]struct{}, len(want))
	attestations := map[string]string{}
	for _, candidate := range candidates {
		for _, fingerprint := range candidate.fingerprints {
			if slices.Contains(held, fingerprint) {
				if !candidate.attested {
					continue
				}
				if _, named := attestations[fingerprint]; !named {
					attestations[fingerprint] = candidate.attestation
				}
			}
			covered[fingerprint] = struct{}{}
		}
	}
	for fingerprint := range want {
		if _, ok := covered[fingerprint]; !ok {
			return false, nil
		}
	}
	return true, attestations
}

// releaseCandidate reports whether the vault's release ledger lists grant.
func (g *RuleApprovalGate) releaseCandidate(grant hitl.ApprovalGrant) releaseCandidate {
	candidate := releaseCandidate{fingerprints: grant.SecretFingerprints}
	if grant.Attestation != nil && g.sources.ReleaseLedger != nil &&
		g.sources.ReleaseLedger.Covers(grant.ReleaseCoverage(), *grant.Attestation) {
		candidate.attested, candidate.attestation = true, grant.Attestation.ID
	}
	return candidate
}
