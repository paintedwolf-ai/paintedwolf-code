package gitcmd

import (
	"context"
	"net/url"

	"github.com/lycaon/lycaon/internal/gitcredential"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

const savedCredentialShape = "Saved Git credential"

// releaseCredential reviews handing a saved credential to Git for its
// recipient, as any protected value leaving for a recipient is reviewed: the
// decision names the credential and the recipient, so a remembered approval
// covers that pair and nothing else. A fault or anything but sending it
// unchanged keeps the credential.
func (o *Owners) releaseCredential(ctx context.Context, r gitcredential.Recipient, c gitcredential.Credential) bool {
	if o.SecretAsk == nil || o.SecretMatcher == nil || c.Password == "" {
		return false
	}
	match, err := o.SecretMatcher.ManagedValue(c.Password, savedCredentialShape, "")
	if err != nil {
		return false
	}
	id, label := credentialRecipient(r)
	resolution, err := o.SecretAsk(ctx, secretmatch.Alert{
		Surface: secretmatch.SurfaceCommand, DestinationID: id, DestinationLabel: label,
		SecretNames: []string{savedCredentialShape}, RuleID: secretmatch.ManagedRuleID, RuleTitle: savedCredentialShape,
		GenericShape: savedCredentialShape, Occurrences: 1,
		SourceKind: secretmatch.SourceToolArgument, SourceTool: "git", SourcePath: "your saved Git credentials",
		OriginKind: secretmatch.OriginField, Fingerprints: []secretmatch.SecretFingerprint{match.Fingerprint},
		Container: savedCredentialShape,
	})
	return err == nil && resolution.Decision == secretmatch.SendUnchanged
}

func credentialRecipient(r gitcredential.Recipient) (id, label string) {
	return secretmatch.HTTPDestination(&url.URL{Scheme: r.Protocol, Host: r.Host})
}

// credentialDenied is the refusal a network command settles with when Git
// failed after a saved credential it asked for was kept.
func credentialDenied(form string, declined []string) *toolrejection.ToolReject {
	if len(declined) == 0 {
		return nil
	}
	u, err := url.Parse(declined[0])
	if err != nil {
		return nil
	}
	_, label := secretmatch.HTTPDestination(u)
	return &toolrejection.ToolReject{Code: toolrejection.OutboundSecretDeniedCode, Data: map[string]any{
		"surface": "command", "rule_id": secretmatch.ManagedRuleID, "host": label, "shape": savedCredentialShape, "command": form,
	}}
}
