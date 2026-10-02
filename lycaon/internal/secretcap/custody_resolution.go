package secretcap

import (
	"slices"
	"sort"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// HeldValue is one person-held value an invocation resolved.
type HeldValue struct {
	SecretID    string
	Version     int64
	Name        string
	Fingerprint secretmatch.SecretFingerprint
}

// CustodySummary says who holds the managed values one screened send carries.
type CustodySummary struct {
	// ChatGenerated: every screened identity is a value the host generated for
	// this chat, so nothing outside the chat has held it.
	ChatGenerated bool
	// Held lists the values a person handed to the vault; they leave only
	// while that person has unlocked the chat.
	Held []HeldValue
}

// Custody describes the screened identities this invocation resolved. An
// identity this invocation did not resolve is raw evidence: it carries no
// managed custody and keeps ChatGenerated false.
func (r *Resolution) Custody(fingerprints []secretmatch.SecretFingerprint) CustodySummary {
	if r == nil || len(fingerprints) == 0 {
		return CustodySummary{}
	}
	byFingerprint := make(map[secretmatch.SecretFingerprint]resolvedValue, len(r.values))
	for _, value := range r.values {
		if value.fingerprint != "" {
			byFingerprint[value.fingerprint] = value
		}
	}
	summary := CustodySummary{ChatGenerated: true}
	for _, fingerprint := range fingerprints {
		value, ok := byFingerprint[fingerprint]
		if !ok || !value.chatGenerated {
			summary.ChatGenerated = false
		}
		if ok && value.custody.Held() {
			summary.Held = append(summary.Held, HeldValue{
				SecretID: value.id, Version: value.version, Name: value.name, Fingerprint: value.fingerprint,
			})
		}
	}
	sort.Slice(summary.Held, func(i, j int) bool { return summary.Held[i].SecretID < summary.Held[j].SecretID })
	summary.Held = slices.CompactFunc(summary.Held, func(a, b HeldValue) bool { return a.SecretID == b.SecretID })
	return summary
}

// HoldsPersonValues reports whether this invocation resolved any value a
// person holds, wherever it appears.
func (r *Resolution) HoldsPersonValues() bool {
	if r == nil {
		return false
	}
	for _, value := range r.values {
		if value.custody.Held() {
			return true
		}
	}
	return false
}

// heldWithoutRelease names the selected person-held values that no reviewed
// handoff covers and no redaction removed. Callers hold r.mu.
func (r *Resolution) heldWithoutRelease(included func(string) bool) (names []string, held bool) {
	for id := range r.selectedIDs(included) {
		value := r.values[id]
		if !value.custody.Held() || r.delivery[id].redacted {
			continue
		}
		held = true
		if _, released := r.releasedToLocked(value.fingerprint); !released {
			names = append(names, value.name)
		}
	}
	sort.Strings(names)
	return names, held
}
