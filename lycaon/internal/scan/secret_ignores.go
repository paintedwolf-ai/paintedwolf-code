package scan

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectignore"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// SecretIgnoreSource supplies active exact-value declarations for an attributed scan root.
type SecretIgnoreSource func(context.Context, string) map[string]projectignore.SecretEntry

func (s *SQLStore) SaveSecretIdentities(ctx context.Context, scanID string, result *scanoutput.Result) error {
	if result == nil {
		return nil
	}
	return s.inTx(ctx, func(q *db.Queries, _ *sql.Tx) error {
		if err := q.DeleteScanSecretIdentities(ctx, scanID); err != nil {
			return err
		}
		seen := map[int]bool{}
		for _, identity := range result.SecretIdentities {
			seen[identity.FindingIndex] = true
			if identity.FindingIndex < 0 || identity.FindingIndex >= len(result.Findings) || identity.ValueFingerprint == "" || scanfindings.FindingKind(result.Findings[identity.FindingIndex]) != api.FindingKindSecret {
				return fmt.Errorf("invalid private secret finding identity")
			}
			if err := q.PutScanSecretIdentity(ctx, db.PutScanSecretIdentityParams{ScanID: scanID, FindingFingerprint: FindingIdentity(result.Findings[identity.FindingIndex]), ValueFingerprint: identity.ValueFingerprint}); err != nil {
				return err
			}
		}
		for index, finding := range result.Findings {
			if seen[index] || scanfindings.FindingKind(finding) != api.FindingKindSecret {
				continue
			}
			if err := q.PutScanSecretIdentity(ctx, db.PutScanSecretIdentityParams{ScanID: scanID, FindingFingerprint: FindingIdentity(finding), ValueFingerprint: ""}); err != nil {
				return err
			}
		}
		return nil
	})
}

func secretIgnoreDecision(values []string, decisions map[string]projectignore.SecretEntry) (scanignore.IgnoreRule, bool) {
	if len(values) == 0 {
		return scanignore.IgnoreRule{}, false
	}
	var chosen projectignore.SecretEntry
	for _, value := range values {
		entry, ok := decisions[value]
		if !ok {
			return scanignore.IgnoreRule{}, false
		}
		if chosen.Value == "" {
			chosen = entry
		}
	}
	return scanignore.IgnoreRule{IgnoreEntry: scanignore.IgnoreEntry{ID: "secret:" + chosen.Key(), Reason: chosen.Reason, Expires: chosen.Expires}, Source: scanignore.IgnoreSourceProject}, true
}

func partitionSecretIgnores(result *scanoutput.Result, findings []api.SecurityFinding, decisions map[string]projectignore.SecretEntry) ([]api.SecurityFinding, []scanignore.IgnoredFinding) {
	identities := map[string][]string{}
	seen := map[int]bool{}
	for _, identity := range result.SecretIdentities {
		if identity.FindingIndex < 0 || identity.FindingIndex >= len(result.Findings) {
			continue
		}
		seen[identity.FindingIndex] = true
		key := FindingIdentity(result.Findings[identity.FindingIndex])
		identities[key] = append(identities[key], identity.ValueFingerprint)
	}
	for index, finding := range result.Findings {
		if !seen[index] {
			key := FindingIdentity(finding)
			identities[key] = append(identities[key], "")
		}
	}
	active := make([]api.SecurityFinding, 0, len(findings))
	var ignored []scanignore.IgnoredFinding
	for _, finding := range findings {
		key := FindingIdentity(finding)
		rule, ok := secretIgnoreDecision(identities[key], decisions)
		if !ok || scanfindings.FindingKind(finding) != api.FindingKindSecret {
			active = append(active, finding)
			continue
		}
		ignored = append(ignored, scanignore.IgnoredFinding{Fingerprint: key, RuleID: finding.RuleID, EntryID: rule.ID, MatchedOn: "secrets: exact value", Reason: rule.Reason, Expires: rule.Expires})
	}
	return active, ignored
}

func secretDecisionDigest(decisions map[string]projectignore.SecretEntry) string {
	// Scan results retain a digest without the declared values.
	data, err := surveyjson.Marshal(decisions)
	if err != nil {
		panic("secret decision digest: " + err.Error())
	}
	return projectignore.Digest(data)
}

func splitSecretIdentities(values string) []string {
	if values == "" {
		return nil
	}
	return strings.Split(values, ",")
}
