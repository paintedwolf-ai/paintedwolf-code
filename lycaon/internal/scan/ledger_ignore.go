package scan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/projectignore"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
)

// SyncIgnores refreshes cached verdicts when the declaration digest changes.
func (s *SQLStore) SyncIgnores(ctx context.Context, canonicalPath string, rootPaths []string) (*scanignore.IgnoreCatalog, error) {
	catalog, secretDecisions, err := s.ignoreDeclarations(ctx, canonicalPath, rootPaths)
	if err != nil {
		return nil, err
	}
	applied, err := s.queries.GetScanIgnoreDigest(ctx, canonicalPath)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read ignore digest: %w", err)
	}
	if applied == catalog.Digest {
		return catalog, nil
	}
	if err := s.applyIgnores(ctx, canonicalPath, catalog, secretDecisions); err != nil {
		return nil, err
	}
	return catalog, nil
}

// IgnoreMatches reads the declared ignores and counts the ledger rows each one
// matches now, without recording the verdicts.
func (s *SQLStore) IgnoreMatches(ctx context.Context, canonicalPath string, rootPaths []string) (*scanignore.IgnoreCatalog, map[string]int, error) {
	catalog, secretDecisions, err := s.ignoreDeclarations(ctx, canonicalPath, rootPaths)
	if err != nil {
		return nil, nil, err
	}
	subjects, err := s.queries.ListScanFindingLedgerSubjects(ctx, canonicalPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read ledger subjects: %w", err)
	}
	matches := map[string]int{}
	for _, subject := range subjects {
		if rule, _, ok := ignoreVerdict(subject, catalog, secretDecisions); ok {
			matches[IgnoreEntryKey(rule)]++
		}
	}
	return catalog, matches, nil
}

// ignoreDeclarations are the overlay ignore catalog and the secret decisions,
// with a digest over both.
func (s *SQLStore) ignoreDeclarations(ctx context.Context, canonicalPath string, rootPaths []string) (*scanignore.IgnoreCatalog, map[string]projectignore.SecretEntry, error) {
	catalog, err := scanignore.LoadIgnoreCatalog(rootPaths)
	if err != nil {
		return nil, nil, err
	}
	var secretDecisions map[string]projectignore.SecretEntry
	if s.SecretIgnores != nil {
		secretDecisions = s.SecretIgnores(ctx, canonicalPath)
	}
	catalog.Digest += secretDecisionDigest(secretDecisions)
	return catalog, secretDecisions, nil
}

// ignoreVerdict is the declaration that ignores subject, if any. It matches at
// the zero time so lapsed entries still count and show as expired.
func ignoreVerdict(subject db.ListScanFindingLedgerSubjectsRow, catalog *scanignore.IgnoreCatalog, secretDecisions map[string]projectignore.SecretEntry) (scanignore.IgnoreRule, string, bool) {
	rule, ok := catalog.Match(scanignore.IgnoreSubject{
		Path:        subject.Uri,
		Kind:        subject.Kind,
		ScannerID:   subject.ScannerID,
		RuleID:      subject.RuleID,
		AdvisoryIDs: splitAdvisoryIndex(subject.AdvisoryIds),
		Fingerprint: subject.Fingerprint,
	}, time.Time{})
	matchedOn := rule.MatchedOn()
	if subject.Kind == "secret" {
		if secretRule, accepted := secretIgnoreDecision(splitSecretIdentities(subject.ValueFingerprints), secretDecisions); accepted {
			return secretRule, "secrets: exact value", true
		}
	}
	return rule, matchedOn, ok
}

// Clearing cached verdicts restores findings whose declarations were removed.
func (s *SQLStore) applyIgnores(ctx context.Context, canonicalPath string, catalog *scanignore.IgnoreCatalog, secretDecisions map[string]projectignore.SecretEntry) error {
	subjects, err := s.queries.ListScanFindingLedgerSubjects(ctx, canonicalPath)
	if err != nil {
		return fmt.Errorf("read ledger subjects: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply ignores: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.queries.WithTx(tx)

	if err := qtx.ClearScanFindingLedgerIgnores(ctx, canonicalPath); err != nil {
		return fmt.Errorf("clear ledger ignores: %w", err)
	}
	for _, subject := range subjects {
		rule, matchedOn, ok := ignoreVerdict(subject, catalog, secretDecisions)
		if !ok {
			continue
		}
		if err := qtx.SetScanFindingLedgerIgnore(ctx, db.SetScanFindingLedgerIgnoreParams{
			IgnoreEntryID:       IgnoreEntryKey(rule),
			IgnoreReason:        rule.Reason,
			IgnoreMatchedOn:     matchedOn,
			IgnoreJustification: rule.Justification,
			IgnoreExpires:       strings.TrimSpace(rule.Expires),
			CanonicalPath:       canonicalPath,
			ScannerID:           subject.ScannerID,
			Fingerprint:         subject.Fingerprint,
		}); err != nil {
			return fmt.Errorf("apply ledger ignore: %w", err)
		}
	}
	if err := qtx.PutScanIgnoreDigest(ctx, db.PutScanIgnoreDigestParams{
		CanonicalPath: canonicalPath,
		Digest:        catalog.Digest,
		AppliedAt:     db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return fmt.Errorf("record ignore digest: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("apply ignores: %w", err)
	}
	return nil
}

// InvalidateIgnoreDigest forces the next read to re-apply.
func (s *SQLStore) InvalidateIgnoreDigest(ctx context.Context, canonicalPath string) error {
	if err := s.queries.PutScanIgnoreDigest(ctx, db.PutScanIgnoreDigestParams{
		CanonicalPath: canonicalPath,
		Digest:        "",
		AppliedAt:     db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return fmt.Errorf("invalidate ignore digest: %w", err)
	}
	return nil
}

// IgnoreEntryKey is the id a row records; entries without one use source and predicates.
func IgnoreEntryKey(rule scanignore.IgnoreRule) string {
	if id := strings.TrimSpace(rule.ID); id != "" {
		return id
	}
	return string(rule.Source) + ":" + rule.MatchedOn()
}

// splitAdvisoryIndex reverses advisoryIDIndex.
func splitAdvisoryIndex(index string) []string {
	fields := strings.Fields(index)
	if len(fields) == 0 {
		return nil
	}
	return fields
}
