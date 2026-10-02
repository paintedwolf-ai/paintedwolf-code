package secretcap

import (
	"slices"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// Release is one reviewed handoff of resolved values to their recipients. It
// lasts for this immutable invocation, including when the person chose once.
type Release struct {
	Fingerprints []secretmatch.SecretFingerprint
	Recipients   []secretmatch.Recipient
	// AttestationID names the presence attestation that released person-held
	// values. A release without one never covers a held value.
	AttestationID string
}

// ApproveRelease records a reviewed handoff, including a compound capability review.
func (r *Resolution) ApproveRelease(release Release) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.permissions = append(r.permissions, Release{
		Fingerprints:  append([]secretmatch.SecretFingerprint(nil), release.Fingerprints...),
		Recipients:    append([]secretmatch.Recipient(nil), release.Recipients...),
		AttestationID: release.AttestationID,
	})
}

// ApproveAttested records one reviewed handoff in which each held fingerprint
// names the attestation that released it, so every use records its own.
func (r *Resolution) ApproveAttested(fingerprints []secretmatch.SecretFingerprint, recipients []secretmatch.Recipient, attestations map[string]string) {
	groups := map[string][]secretmatch.SecretFingerprint{}
	for _, fingerprint := range fingerprints {
		id := attestations[string(fingerprint)]
		groups[id] = append(groups[id], fingerprint)
	}
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		r.ApproveRelease(Release{Fingerprints: groups[id], Recipients: recipients, AttestationID: id})
	}
}

// UseCovered reports whether an approved handoff covers fingerprint for
// recipient. A person-held value is covered only by an attested release.
func (r *Resolution) UseCovered(fingerprint secretmatch.SecretFingerprint, recipient secretmatch.Recipient) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	held := r.heldLocked(fingerprint)
	for _, release := range r.permissions {
		if held && release.AttestationID == "" {
			continue
		}
		if secretmatch.RecipientCovered(release.Recipients, recipient.ID, string(recipient.Surface)) &&
			slices.Contains(release.Fingerprints, fingerprint) {
			return true
		}
	}
	return false
}

// heldLocked reports whether fingerprint names a person-held value this
// invocation resolved. Callers hold r.mu.
func (r *Resolution) heldLocked(fingerprint secretmatch.SecretFingerprint) bool {
	for _, value := range r.values {
		if value.fingerprint == fingerprint && value.custody.Held() {
			return true
		}
	}
	return false
}

// releaseOfLocked returns the recipients and attestation that released
// fingerprint. Callers hold r.mu.
func (r *Resolution) releaseOfLocked(fingerprint secretmatch.SecretFingerprint) ([]secretmatch.Recipient, string) {
	var recipients []secretmatch.Recipient
	attestation := ""
	for _, release := range r.permissions {
		if !slices.Contains(release.Fingerprints, fingerprint) {
			continue
		}
		recipients = append(recipients, release.Recipients...)
		if attestation == "" {
			attestation = release.AttestationID
		}
	}
	return recipients, attestation
}

// ApproveLocalConnections records reviewed local ports for this invocation.
func (r *Resolution) ApproveLocalConnections(ports []uint16) {
	if r == nil || len(ports) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.localConnectPorts = append(r.localConnectPorts, ports...)
}

// LocalConnectionsCovered reports whether every port was approved.
func (r *Resolution) LocalConnectionsCovered(ports []uint16) bool {
	if r == nil || len(ports) == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, port := range ports {
		if !slices.Contains(r.localConnectPorts, port) {
			return false
		}
	}
	return true
}

// RecipientApproved supplies consent to first-party HTTP requests only.
func (r *Resolution) RecipientApproved(recipient secretmatch.Recipient) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, release := range r.permissions {
		if len(release.Fingerprints) > 0 && secretmatch.RecipientCovered(release.Recipients, recipient.ID, string(recipient.Surface)) {
			return true
		}
	}
	return false
}
