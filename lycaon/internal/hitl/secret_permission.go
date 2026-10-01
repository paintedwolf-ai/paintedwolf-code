package hitl

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

func SecretReleaseWitness(recipients []secretmatch.Recipient) ApprovalGrantWitness {
	return ApprovalGrantWitness{Egress: secretmatch.RecipientDigest(recipients)}
}

// ValidateSecretRelease applies equally to chat and persisted permissions.
func ValidateSecretRelease(grant ApprovalGrant) error {
	recipients, err := secretmatch.CanonicalRecipients(grant.SecretRecipients)
	if err != nil {
		return err
	}
	if len(recipients) != len(grant.SecretRecipients) || !WitnessEqual(grant.Witness, SecretReleaseWitness(recipients)) {
		return fmt.Errorf("secret permission has inconsistent recipient authority")
	}
	if strings.TrimSpace(grant.ProjectID) == "" || grant.SecretDestinationID != "" {
		return fmt.Errorf("secret permission requires project identity and explicit recipients")
	}
	if grant.Scope == ApprovalGrantScopeChat && strings.TrimSpace(grant.ChatSessionID) == "" {
		return fmt.Errorf("secret task permission requires task identity")
	}
	if grant.Scope != ApprovalGrantScopeChat && grant.Scope != ApprovalGrantScopeProject {
		return fmt.Errorf("secret release cannot have device scope")
	}
	fingerprints := make([]secretmatch.SecretFingerprint, 0, len(grant.SecretFingerprints))
	for _, value := range grant.SecretFingerprints {
		if value == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("secret permission requires canonical fingerprints")
		}
		fingerprints = append(fingerprints, secretmatch.SecretFingerprint(value))
	}
	if len(fingerprints) == 0 || grant.Predicate.Pattern != secretmatch.FingerprintDigest(fingerprints) {
		return fmt.Errorf("secret permission fingerprint set disagrees with its identity")
	}
	return nil
}

func (permission SecretPermission) validate(action ProposedAction) error {
	witness := SecretReleaseWitness(permission.Screen.Recipients)
	chatWitness := chatReleaseWitness(permission.Screen.Recipients)
	pattern := secretmatch.FingerprintDigest(permission.Fingerprints)
	if len(permission.Fingerprints) == 0 || len(permission.Offers) == 0 {
		return fmt.Errorf("compound secret permission has no protected values or release choices")
	}
	for _, offer := range permission.Offers {
		grant := offer.Grant
		if err := ValidateSecretRelease(grant); err != nil {
			return err
		}
		expectedWitness := witness
		if grant.Scope == ApprovalGrantScopeChat {
			expectedWitness = chatWitness
		}
		if grant.Predicate.Category != ApprovalGrantCategorySecret || grant.Predicate.Pattern != pattern ||
			(!WitnessEqual(grant.Witness, witness) && !WitnessEqual(grant.Witness, expectedWitness)) || grant.ProjectID != action.ProjectID ||
			(grant.Scope == ApprovalGrantScopeChat && grant.ChatSessionID != action.ChatSession() && grant.ChatSessionID != action.ChatSession()) {
			return fmt.Errorf("compound secret permission disagrees with the reviewed handoff")
		}
	}
	return nil
}

func chatReleaseWitness(recipients []secretmatch.Recipient) ApprovalGrantWitness {
	chatRecipients := recipients
	hasLocal := false
	for _, r := range recipients {
		if r.IsLocal() {
			hasLocal = true
			break
		}
	}
	if hasLocal {
		merged := append([]secretmatch.Recipient{secretmatch.LocalRecipient}, recipients...)
		if can, err := secretmatch.CanonicalRecipients(merged); err == nil {
			chatRecipients = can
		}
	}
	return SecretReleaseWitness(chatRecipients)
}
