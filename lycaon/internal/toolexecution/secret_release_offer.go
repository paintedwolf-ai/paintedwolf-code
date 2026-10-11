package toolexecution

import (
	"crypto/sha256"
	"encoding/base64"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

type secretFingerprintSet struct {
	pattern string
	values  []string
}

func canonicalSecretFingerprintSet(fingerprints []secretmatch.SecretFingerprint) (secretFingerprintSet, bool) {
	seen := make(map[secretmatch.SecretFingerprint]struct{}, len(fingerprints))
	for _, fingerprint := range fingerprints {
		if fingerprint != "" {
			seen[fingerprint] = struct{}{}
		}
	}
	identities := make([]secretmatch.SecretFingerprint, 0, len(seen))
	for fingerprint := range seen {
		identities = append(identities, fingerprint)
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i] < identities[j] })
	values := make([]string, 0, len(identities))
	for _, fingerprint := range identities {
		values = append(values, string(fingerprint))
	}
	if len(values) == 0 {
		return secretFingerprintSet{}, false
	}
	return secretFingerprintSet{pattern: secretmatch.FingerprintDigest(identities), values: values}, true
}

func secretGrantID(parts ...string) string {
	identity := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "grant_" + base64.RawURLEncoding.EncodeToString(identity[:])
}

func newSecretReleaseOffer(projectID, projectDir string, recipients []secretmatch.Recipient, names []string, fingerprints []secretmatch.SecretFingerprint) (hitl.ApprovalGrantOffer, bool) {
	projectID = strings.TrimSpace(projectID)
	projectDir = strings.TrimSpace(projectDir)
	canonical, err := secretmatch.CanonicalRecipients(recipients)
	if projectID == "" || err != nil || len(fingerprints) == 0 {
		return hitl.ApprovalGrantOffer{}, false
	}
	set, ok := canonicalSecretFingerprintSet(fingerprints)
	if !ok {
		return hitl.ApprovalGrantOffer{}, false
	}
	unscreened := slices.Contains(set.values, string(secretmatch.UnscreenedFingerprint))
	valueCount := len(set.values)
	if unscreened {
		valueCount--
	}
	coverage := secretPermissionCoverage(names, canonical, valueCount, unscreened)
	grant := hitl.ApprovalGrant{
		Scope:              hitl.ApprovalGrantScopeProject,
		Predicate:          hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySecret, Pattern: set.pattern},
		ProjectID:          projectID,
		ProjectDir:         projectDir,
		SecretRecipients:   canonical,
		SecretNames:        append([]string(nil), names...),
		Witness:            hitl.SecretReleaseWitness(canonical),
		Coverage:           coverage,
		GrantedAt:          time.Now().UTC(),
		ReaskWhen:          "a different secret, destination, surface, or project is involved",
		SecretFingerprints: set.values,
	}
	grant.ID = secretGrantID(string(grant.Scope), grant.Predicate.Category, set.pattern, projectID, secretmatch.RecipientDigest(canonical))
	base := hitl.ApprovalGrantOffer{
		ID: grant.ID, Scope: grant.Scope, Coverage: coverage,
		ReaskWhen: grant.ReaskWhen, Subject: gate.ReusePredicate, Grant: grant,
	}
	return base, true
}

func releaseTitles(managed bool) (day, chat, project string) {
	if managed {
		return hitl.TitleSendFor1Day, hitl.TitleSendForThisChat, hitl.TitleSendForThisProject
	}
	return hitl.TitleSendUnchangedFor1Day, hitl.TitleSendUnchangedForThisChat, hitl.TitleSendUnchangedForThisProject
}

func secretReleaseLadder(chatSessionID, projectID, projectDir string, recipients []secretmatch.Recipient, names []string, managed bool, fingerprints []secretmatch.SecretFingerprint) []hitl.ApprovalGrantOffer {
	dayTitle, chatTitle, projectTitle := releaseTitles(managed)
	base, ok := newSecretReleaseOffer(projectID, projectDir, recipients, names, fingerprints)
	if !ok {
		return nil
	}
	day := hitl.DayRung(base)
	day.Title = dayTitle
	day.Grant.Title = day.Title
	day.Grant.ExpiresWhen = day.ExpiresWhen

	var chat *hitl.ApprovalGrantOffer
	if chatSessionID = strings.TrimSpace(chatSessionID); chatSessionID != "" {
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
		chatBase, ok := newSecretReleaseOffer(projectID, projectDir, chatRecipients, names, fingerprints)
		if !ok {
			chatBase = base
		}
		offer := chatBase
		offer.Rung = hitl.ApprovalRungChat
		offer.Scope = hitl.ApprovalGrantScopeChat
		offer.Title = chatTitle
		offer.ExpiresWhen = hitl.ExpiresWhenChatDeleted
		offer.ID = secretGrantID(offer.ID, "chat", chatSessionID)
		offer.Grant.ID = offer.ID
		offer.Grant.Scope = hitl.ApprovalGrantScopeChat
		offer.Grant.ChatSessionID = chatSessionID
		offer.Grant.Title = offer.Title
		offer.Grant.ExpiresWhen = offer.ExpiresWhen
		chat = &offer
	}

	project := base
	project.Rung = hitl.ApprovalRungProject
	project.Title = projectTitle
	project.ExpiresWhen = hitl.ExpiresIn7DaysOrRevoked
	project.ID = base.ID + "-project"
	project.Grant.ID = project.ID
	project.Grant.Title = project.Title
	project.Grant.ExpiresWhen = project.ExpiresWhen
	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	project.Grant.ExpiresAt = &expires

	out := []hitl.ApprovalGrantOffer{day}
	if chat != nil {
		out = append(out, *chat)
	}
	return append(out, project)
}

// Provider trust also releases the currently held send.
func trustProviderOffer(providerID, destinationID, label string) hitl.ApprovalGrantOffer {
	label = strings.TrimSpace(label)
	if label == "" {
		label = strings.TrimSpace(providerID)
	}
	grant := hitl.ApprovalGrant{
		Scope:               hitl.ApprovalGrantScopeDevice,
		Predicate:           hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategoryTrustDestination, Pattern: strings.TrimSpace(destinationID)},
		Title:               hitl.TitleTrustProvider(label),
		Coverage:            hitl.CoverageTrustProvider(label),
		GrantedAt:           time.Now().UTC(),
		ExpiresWhen:         hitl.ExpiresWhenUntrusted,
		ReaskWhen:           hitl.ReaskWhenDifferentProvider,
		SecretDestinationID: strings.TrimSpace(destinationID),
		Source:              "checkpoint",
	}
	grant.ID = secretGrantID(hitl.ApprovalGrantCategoryTrustDestination, strings.TrimSpace(providerID), strings.TrimSpace(destinationID))
	trust := &hitl.TrustDestinationDelta{ProviderID: strings.TrimSpace(providerID), DestinationID: strings.TrimSpace(destinationID), Label: label}
	return hitl.ApprovalGrantOffer{
		ID: grant.ID, Rung: hitl.ApprovalRungDevice, Scope: hitl.ApprovalGrantScopeDevice, Group: hitl.GroupTrust,
		Title: grant.Title, Coverage: grant.Coverage, ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen,
		Subject: gate.ReusePredicate, Grant: grant,
		Authority: []hitl.ApprovalAuthorityDelta{
			{Kind: hitl.AuthorityCurrentAction},
			{Kind: hitl.AuthorityTrustDestination, Grant: &grant, TrustDestination: trust},
		},
	}
}

func secretRedactLadder(projectID, projectDir string, fingerprints []secretmatch.SecretFingerprint) []hitl.ApprovalGrantOffer {
	projectID = strings.TrimSpace(projectID)
	projectDir = strings.TrimSpace(projectDir)
	if projectID == "" {
		return nil
	}
	set, ok := canonicalSecretFingerprintSet(fingerprints)
	if !ok {
		return nil
	}
	// One device-wide redaction option keeps the approval slot stable.
	const lifetime = 30 * 24 * time.Hour
	offer := hitl.ApprovalGrantOffer{
		Scope: hitl.ApprovalGrantScopeDevice, Rung: hitl.ApprovalRungDevice, Group: hitl.GroupRedaction,
		Title:    hitl.TitleKeepRedactingThisDevice,
		Coverage: "this credential, stripped from every send on this device", ExpiresWhen: hitl.ExpiresIn30DaysOrRevoked,
		ReaskWhen: "a different secret is involved", Subject: gate.ReusePredicate,
	}
	offer.ID = secretGrantID(hitl.ApprovalGrantCategorySecretRedact, set.pattern) + "-redact-device"
	expires := time.Now().UTC().Add(lifetime)
	offer.Grant = hitl.ApprovalGrant{
		ID: offer.ID, Scope: hitl.ApprovalGrantScopeDevice,
		Predicate: hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySecretRedact, Pattern: set.pattern},
		ProjectID: projectID, ProjectDir: projectDir,
		Title: offer.Title, Coverage: offer.Coverage, GrantedAt: time.Now().UTC(),
		ExpiresWhen: offer.ExpiresWhen, ExpiresAt: &expires, ReaskWhen: offer.ReaskWhen,
		SecretFingerprints: set.values,
	}
	return []hitl.ApprovalGrantOffer{offer}
}
