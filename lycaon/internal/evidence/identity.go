package evidence

import (
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// PathIdentity is the resolved root identity of an observed path token:
// the owning root's ID plus the path relative to that root. RootID is empty
// for legacy tokens that resolve outside every attached root (or when no
// roots are configured); ScopeRel then carries the legacy normalized token.
type PathIdentity struct {
	RootID   string
	ScopeRel string
}

// ResolvePathIdentity maps a model-visible token to its durable identity
// using the session-folder rule: @label/ tokens resolve to the named root,
// absolute paths to the most specific containing root, and bare tokens to
// the session's active root. Tokens that resolve under no attached root —
// including approved external roots — keep the legacy normalized spelling
// with an empty RootID so external and unresolvable paths behave as before.
func ResolvePathIdentity(roots CitationRoots, token string) PathIdentity {
	token = NormalizeLedgerPath(token)
	if token == "" || sandbox.HasParentTraversal(token) {
		return PathIdentity{}
	}
	if len(roots.Roots) > 0 {
		if abs, root, err := projectroot.ResolveAbs(roots.Roots, roots.ActiveRootID, token); err == nil && !root.External {
			return PathIdentity{RootID: root.ID, ScopeRel: projectroot.ScopeRel(root, abs)}
		}
	}
	if rel, ok := NormalizeCitationPath(roots.ProjectDir, token); ok {
		return PathIdentity{ScopeRel: rel}
	}
	// Without a project directory, bare relative tokens are still ledger keys.
	if strings.TrimSpace(roots.ProjectDir) == "" {
		if rel := NormalizeLedgerPath(token); rel != "" && !sandbox.HasParentTraversal(rel) {
			return PathIdentity{ScopeRel: rel}
		}
	}
	return PathIdentity{}
}

// resolveScopeRel maps a token to its durable ledger key: the root-relative
// path when it resolves under an attached root, else the legacy normalized
// token. The second result reports whether the token is a ledger key at all.
func resolveScopeRel(roots CitationRoots, token string) (string, bool) {
	id := ResolvePathIdentity(roots, token)
	if id.ScopeRel == "" {
		return "", false
	}
	return id.ScopeRel, true
}

// pathRootID returns the owning root of a stored path key on rec: the
// per-path override when present, else the record's primary root.
func (rec Record) pathRootID(key string) string {
	if id, ok := rec.pathRoots[key]; ok {
		return id
	}
	return rec.RootID
}

// StampDisplayKeys computes the session-folder display token for every
// indexed path on rec against the session's current active root, so ledger
// assembly keys rooted records by (root_id, scope_rel) rendered in the
// session folder. Records without rooted identity keep their stored keys.
func StampDisplayKeys(roots CitationRoots, rec *Record) {
	if rec == nil || len(roots.Roots) == 0 {
		return
	}
	for _, key := range IndexedPathsForRecord(*rec) {
		display := displayKeyForStoredPath(roots, *rec, key)
		if display == "" || display == key {
			continue
		}
		if rec.pathDisplay == nil {
			rec.pathDisplay = make(map[string]string)
		}
		rec.pathDisplay[key] = display
	}
}

// displayKeyForStoredPath renders a stored path key in the session folder.
// Legacy keys (empty root) display as stored; rooted keys render bare off
// the active root and @label-qualified off any other attached root.
func displayKeyForStoredPath(roots CitationRoots, rec Record, key string) string {
	rootID := rec.pathRootID(key)
	if rootID == "" {
		return key
	}
	return projectroot.DisplayPath(roots.Roots, roots.ActiveRootID, rootID, key)
}

// DisplayPathsForRecord returns the session-folder display token for every
// indexed path on rec, falling back to the stored key when the record
// carries no display stamp.
func DisplayPathsForRecord(rec Record) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(rec.pathsTouched)+1)
	add := func(stored string) {
		stored = NormalizeLedgerPath(stored)
		if stored == "" {
			return
		}
		display := stored
		if keyed, ok := rec.pathDisplay[stored]; ok && keyed != "" {
			display = keyed
		}
		display = NormalizeLedgerPath(display)
		if display == "" {
			return
		}
		if _, ok := seen[display]; ok {
			return
		}
		seen[display] = struct{}{}
		out = append(out, display)
	}
	add(rec.Path)
	for _, p := range rec.pathsTouched {
		add(p)
	}
	for p := range rec.grepLines {
		add(p)
	}
	return out
}
