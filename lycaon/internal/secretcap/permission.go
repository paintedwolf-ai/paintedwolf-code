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
}

// ApproveRelease records a reviewed handoff, including a compound capability review.
func (r *Resolution) ApproveRelease(release Release) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.permissions = append(r.permissions, Release{
		Fingerprints: append([]secretmatch.SecretFingerprint(nil), release.Fingerprints...),
		Recipients:   append([]secretmatch.Recipient(nil), release.Recipients...),
	})
}

// UseCovered reports whether an approved handoff covers fingerprint for recipient.
func (r *Resolution) UseCovered(fingerprint secretmatch.SecretFingerprint, recipient secretmatch.Recipient) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, release := range r.permissions {
		if secretmatch.RecipientCovered(release.Recipients, recipient.ID, string(recipient.Surface)) &&
			slices.Contains(release.Fingerprints, fingerprint) {
			return true
		}
	}
	return false
}

// releasedToLocked returns the recipients the invocation's reviewed handoffs
// named for fingerprint, and whether any did. Callers hold r.mu.
func (r *Resolution) releasedToLocked(fingerprint secretmatch.SecretFingerprint) ([]secretmatch.Recipient, bool) {
	var recipients []secretmatch.Recipient
	released := false
	for _, release := range r.permissions {
		if slices.Contains(release.Fingerprints, fingerprint) {
			recipients = append(recipients, release.Recipients...)
			released = true
		}
	}
	return recipients, released
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
