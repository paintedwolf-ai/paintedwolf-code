package tools

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

func secretScreenRecipients(finding secretmatch.Alert) ([]secretmatch.Recipient, error) {
	label := strings.TrimSpace(finding.DestinationLabel)
	if label == "" {
		label = finding.DestinationID
	}
	recipients := append([]secretmatch.Recipient(nil), finding.Recipients...)
	recipients = append(recipients, secretmatch.Recipient{
		ID: finding.DestinationID, Surface: finding.Surface, Label: label, Kind: finding.Surface.DestinationKind(),
	})
	return secretmatch.CanonicalRecipients(recipients)
}

func (e *DefaultToolExecutor) secretRecipientsCovered(finding secretmatch.Alert, recipients []secretmatch.Recipient, fingerprints []string) bool {
	for _, recipient := range recipients {
		if !e.approvalGate.SecretFingerprintsCovered(secretScreenChatSession(finding), finding.ProjectID,
			recipient.ID, string(recipient.Surface), fingerprints) {
			return false
		}
	}
	return true
}

// secretPermissionCoverage names what a release covers; unscreened marks the
// subject for content the screen could not read.
func secretPermissionCoverage(names []string, recipients []secretmatch.Recipient, count int, unscreened bool) string {
	what := "this secret"
	if count > 1 {
		what = fmt.Sprintf("these %d secrets", count)
	}
	if len(names) > 0 {
		what = strings.Join(names, ", ")
		if count > len(names) {
			what += fmt.Sprintf(" and %d other protected values", count-len(names))
		}
	}
	if unscreened {
		if count == 0 {
			what = "images the host could not screen"
		} else {
			what += " and images the host could not screen"
		}
	}
	labels := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		labels = append(labels, recipient.Label+" ("+recipient.Surface.Label()+")")
	}
	return "use of " + what + " with " + strings.Join(labels, "; ")
}
