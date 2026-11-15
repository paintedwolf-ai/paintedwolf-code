package touchedpaths

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

const MaxCandidates = 64

// Path is a canonical workspace file identity within an attached root.
type Path struct {
	RootID  string
	RelPath string
}

type LedgerSource interface {
	LoadLedger(ctx context.Context, sessionID string) (evidence.Ledger, error)
}

type AuthoredSource interface {
	SessionAuthoredPaths(ctx context.Context, projectID, sessionID, rootID string) ([]string, error)
}

// List returns up to MaxCandidates deduplicated paths the session authored or observed.
// Authored paths come first (roots in attachment order, paths sorted), followed by
// observed ledger paths (newest handle first).
func List(
	ctx context.Context,
	projectID, sessionID, activeRootID string,
	roots []projectroot.RootRef,
	ledger LedgerSource,
	authored AuthoredSource,
) []Path {
	activeID := resolveActiveRootID(roots, activeRootID)
	rootsByID := make(map[string]projectroot.RootRef, len(roots))
	for _, r := range roots {
		rootsByID[r.ID] = r
	}

	canonicalize := func(rootID, raw string) (Path, bool) {
		rel := evidence.NormalizeLedgerPath(raw)
		if rel == "" || rel == "." || sandbox.HasParentTraversal(rel) {
			return Path{}, false
		}
		if rootID != "" {
			if r, ok := rootsByID[rootID]; ok && r.External {
				return Path{}, false
			}
			return Path{RootID: rootID, RelPath: rel}, true
		}
		if rest, ok := strings.CutPrefix(rel, "@"); ok {
			label, sub, hasSlash := strings.Cut(rest, "/")
			if !hasSlash || label == "" || projectroot.IsVirtualRootLabel(label) {
				return Path{}, false
			}
			sub = evidence.NormalizeLedgerPath(sub)
			if sub == "" || sub == "." || sandbox.HasParentTraversal(sub) {
				return Path{}, false
			}
			for _, r := range roots {
				if !r.External && strings.EqualFold(r.Label, label) {
					return Path{RootID: r.ID, RelPath: sub}, true
				}
			}
			return Path{}, false
		}
		if activeID != "" {
			return Path{RootID: activeID, RelPath: rel}, true
		}
		return Path{RelPath: rel}, true
	}

	seen := make(map[Path]struct{})
	var out []Path
	add := func(p Path) bool {
		if len(out) >= MaxCandidates {
			return false
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			out = append(out, p)
		}
		return len(out) < MaxCandidates
	}

	if authored != nil && sessionID != "" && projectID != "" {
		for _, root := range roots {
			if root.External {
				continue
			}
			rawPaths, err := authored.SessionAuthoredPaths(ctx, projectID, sessionID, root.ID)
			if err != nil || len(rawPaths) == 0 {
				continue
			}
			sorted := make([]string, 0, len(rawPaths))
			for _, raw := range rawPaths {
				if p, ok := canonicalize(root.ID, raw); ok {
					sorted = append(sorted, p.RelPath)
				}
			}
			sort.Strings(sorted)
			for _, rel := range sorted {
				if !add(Path{RootID: root.ID, RelPath: rel}) {
					return out
				}
			}
		}
	}

	if len(out) >= MaxCandidates || ledger == nil || sessionID == "" {
		return out
	}
	ev, err := ledger.LoadLedger(ctx, sessionID)
	if err != nil {
		return out
	}

	handles := evidence.HandlesNewestFirst(ev)
	covered := make(map[string]struct{}, len(handles))
	for _, h := range handles {
		covered[h] = struct{}{}
		rec, ok := ev.Handles[h]
		if !ok || rec.IsGate() || rec.SupersededBy != "" {
			continue
		}
		for _, id := range evidence.PathIdentitiesForRecord(rec) {
			if p, ok := canonicalize(id.RootID, id.ScopeRel); ok {
				if !add(p) {
					return out
				}
			}
		}
	}

	if len(ev.ByPath) > 0 && len(out) < MaxCandidates {
		type fallbackEntry struct {
			key     string
			ordinal int
		}
		var extras []fallbackEntry
		for key, hs := range ev.ByPath {
			allCovered := len(hs) > 0
			bestOrd := 0
			for _, h := range hs {
				if _, ok := covered[h]; !ok {
					allCovered = false
				}
				if _, ord := evidence.ParseHandleOrdinal(h); ord > bestOrd {
					bestOrd = ord
				}
			}
			if !allCovered {
				extras = append(extras, fallbackEntry{key: key, ordinal: bestOrd})
			}
		}
		sort.Slice(extras, func(i, j int) bool {
			if extras[i].ordinal != extras[j].ordinal {
				return extras[i].ordinal > extras[j].ordinal
			}
			return extras[i].key < extras[j].key
		})
		for _, item := range extras {
			if p, ok := canonicalize("", item.key); ok {
				if !add(p) {
					return out
				}
			}
		}
	}

	return out
}

func resolveActiveRootID(roots []projectroot.RootRef, activeRootID string) string {
	if active, err := projectroot.ActiveRoot(roots, activeRootID); err == nil && !active.External {
		return active.ID
	}
	if primary, err := projectroot.PrimaryRoot(roots); err == nil && !primary.External {
		return primary.ID
	}
	return ""
}
