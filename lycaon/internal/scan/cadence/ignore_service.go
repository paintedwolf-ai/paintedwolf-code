package cadence

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/projectignore"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scanignore "github.com/lycaon/lycaon/internal/scan/ignores"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

// IgnoreService serves finding ledger queries and project ignore rules.
type IgnoreService struct {
	Store *scanbase.SQLStore
	// OverlayRootsApply selects trusted roots whose scan settings apply; nil disables overlays.
	OverlayRootsApply func(ctx context.Context, rootPaths []string) []string
}

// ignoreRoots returns the overlay roots the trust gate admits. An untrusted
// project contributes no ignores.
func (s *IgnoreService) ignoreRoots(ctx context.Context, projectDir string) ([]string, error) {
	if err := settingsoverlay.CheckFormat(projectDir); err != nil {
		return nil, err
	}
	if s == nil || s.OverlayRootsApply == nil {
		return nil, nil
	}
	return s.OverlayRootsApply(ctx, []string{projectDir}), nil
}

// ListIgnores reports the catalog with each entry's current ledger match count.
func (s *IgnoreService) ListIgnores(ctx context.Context, projectDir string) (*api.FindingIgnoreListResponse, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("scan cadence not configured")
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return nil, err
	}
	roots, err := s.ignoreRoots(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	catalog, matches, err := s.Store.IgnoreMatches(ctx, canonical, roots)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := &api.FindingIgnoreListResponse{Path: projectignore.Path(projectDir)}
	for _, rule := range catalog.Rules {
		key := scanbase.IgnoreEntryKey(rule)
		entry := toAPIIgnoreEntry(rule.IgnoreEntry)
		// Use the ledger's key so listings and rows agree, even for entries without an id.
		out.Rules = append(out.Rules, api.FindingIgnoreRule{
			ID:            key,
			Path:          entry.Path,
			Kind:          entry.Kind,
			Scanner:       entry.Scanner,
			Rule:          entry.Rule,
			Advisory:      entry.Advisory,
			Fingerprint:   entry.Fingerprint,
			Reason:        entry.Reason,
			Justification: entry.Justification,
			ExpiresOn:     entry.ExpiresOn,
			Source:        string(rule.Source),
			Expired:       rule.Expired(now),
			Withdrawable:  rule.Source == scanignore.IgnoreSourceProject && strings.TrimSpace(rule.ID) != "",
			Matches:       matches[key],
		})
	}
	if out.Rules == nil {
		out.Rules = []api.FindingIgnoreRule{}
	}
	return out, nil
}

func (s *IgnoreService) AddIgnore(ctx context.Context, projectDir string, entry api.FindingIgnoreEntry) (*api.FindingIgnoreListResponse, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("scan cadence not configured")
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return nil, err
	}
	roots, err := s.ignoreRoots(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, scanignore.ErrIgnoreProjectNotTrusted
	}
	if _, err := scanignore.AddIgnoreEntry(projectDir, fromAPIIgnoreEntry(entry)); err != nil {
		return nil, err
	}
	if err := s.Store.InvalidateIgnoreDigest(ctx, canonical); err != nil {
		return nil, err
	}
	return s.ListIgnores(ctx, projectDir)
}

func (s *IgnoreService) RemoveIgnore(ctx context.Context, projectDir, id string) (*api.FindingIgnoreListResponse, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("scan cadence not configured")
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return nil, err
	}
	roots, err := s.ignoreRoots(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, scanignore.ErrIgnoreProjectNotTrusted
	}
	// Map the host key to the file's id; entries without one cannot be removed here.
	catalog, err := scanignore.LoadIgnoreCatalog(roots)
	if err != nil {
		return nil, err
	}
	fileID := ""
	for _, rule := range catalog.Rules {
		if rule.Source == scanignore.IgnoreSourceProject && scanbase.IgnoreEntryKey(rule) == strings.TrimSpace(id) {
			fileID = strings.TrimSpace(rule.ID)
		}
	}
	if fileID == "" {
		return nil, scanignore.ErrIgnoreEntryNotFound
	}
	if err := scanignore.RemoveIgnoreEntry(projectDir, fileID); err != nil {
		return nil, err
	}
	if err := s.Store.InvalidateIgnoreDigest(ctx, canonical); err != nil {
		return nil, err
	}
	return s.ListIgnores(ctx, projectDir)
}

func toAPIIgnoreEntry(entry scanignore.IgnoreEntry) api.FindingIgnoreEntry {
	return api.FindingIgnoreEntry{
		ID:            entry.ID,
		Path:          entry.Path,
		Kind:          api.FindingKind(entry.Kind),
		Scanner:       entry.Scanner,
		Rule:          entry.Rule,
		Advisory:      entry.Advisory,
		Fingerprint:   entry.Fingerprint,
		Reason:        entry.Reason,
		Justification: api.FindingIgnoreJustification(entry.Justification),
		ExpiresOn:     entry.Expires,
	}
}

func fromAPIIgnoreEntry(entry api.FindingIgnoreEntry) scanignore.IgnoreEntry {
	return scanignore.IgnoreEntry{
		ID:            strings.TrimSpace(entry.ID),
		Path:          strings.TrimSpace(entry.Path),
		Kind:          strings.TrimSpace(string(entry.Kind)),
		Scanner:       strings.TrimSpace(entry.Scanner),
		Rule:          strings.TrimSpace(entry.Rule),
		Advisory:      strings.TrimSpace(entry.Advisory),
		Fingerprint:   strings.TrimSpace(entry.Fingerprint),
		Reason:        strings.TrimSpace(entry.Reason),
		Justification: strings.TrimSpace(string(entry.Justification)),
		Expires:       strings.TrimSpace(entry.ExpiresOn),
	}
}

// FindingLedger pages a project's findings across every selected scanner.
func (s *IgnoreService) FindingLedger(ctx context.Context, projectDir string, req api.FindingLedgerQueryRequest) (*api.FindingLedgerResponse, error) {
	if s == nil || s.Store == nil {
		return nil, fmt.Errorf("scan cadence not configured")
	}
	canonical, err := scanbase.CanonicalPath(projectDir)
	if err != nil {
		return nil, err
	}
	// Re-apply ignores if the file changed, so hand edits apply without a rescan.
	roots, err := s.ignoreRoots(ctx, projectDir)
	if err != nil {
		return nil, err
	}
	if _, err := s.Store.SyncIgnores(ctx, canonical, roots); err != nil {
		return nil, err
	}
	return s.Store.FindingLedger(ctx, canonical, req)
}
