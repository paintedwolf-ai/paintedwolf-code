package secretcap

import (
	"slices"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

type invocationPermission struct {
	fingerprints []secretmatch.SecretFingerprint
	recipients   []secretmatch.Recipient
}

// ApproveUse records a reviewed handoff, including a compound capability review.
// It lasts for this immutable invocation, including when the person chose once.
func (r *Resolution) ApproveUse(fingerprints []secretmatch.SecretFingerprint, recipients []secretmatch.Recipient) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.permissions = append(r.permissions, invocationPermission{
		fingerprints: append([]secretmatch.SecretFingerprint(nil), fingerprints...),
		recipients:   append([]secretmatch.Recipient(nil), recipients...),
	})
}

// UseCovered reports whether an approved handoff covers fingerprint for recipient.
func (r *Resolution) UseCovered(fingerprint secretmatch.SecretFingerprint, recipient secretmatch.Recipient) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, permission := range r.permissions {
		if secretmatch.RecipientCovered(permission.recipients, recipient.ID, string(recipient.Surface)) &&
			slices.Contains(permission.fingerprints, fingerprint) {
			return true
		}
	}
	return false
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
	for _, permission := range r.permissions {
		if len(permission.fingerprints) > 0 && secretmatch.RecipientCovered(permission.recipients, recipient.ID, string(recipient.Surface)) {
			return true
		}
	}
	return false
}
