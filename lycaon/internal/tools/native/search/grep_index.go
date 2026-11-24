package search

import (
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
)

// grepContentIndex owns pruning and capture telemetry for one search.
type grepContentIndex struct {
	literals *sourcecatalog.LiteralSearch
	require  litprefilter.Requirement
	stats    contentSearchStats
}

func newGrepContentIndex(inv *invocation, require litprefilter.Requirement, prune bool) grepContentIndex {
	if inv.owners.Catalog == nil {
		return grepContentIndex{}
	}
	index := grepContentIndex{literals: inv.owners.Catalog.Literals}
	if prune {
		index.require = require
	}
	return index
}

func (index grepContentIndex) target(inv *invocation, e entry) (string, sourcecatalog.Entry, fspath.FileObservation, bool) {
	if index.literals == nil || e.depth <= 0 || e.link || e.followed || !e.file() || e.bound.Resolved.Compressed || strings.HasSuffix(strings.ToLower(e.name), ".gz") {
		return "", sourcecatalog.Entry{}, fspath.FileObservation{}, false
	}
	version := contentVersion(e)
	if !version.Known || version.Links == 0 || version.Size <= 0 {
		return "", sourcecatalog.Entry{}, fspath.FileObservation{}, false
	}
	rootPath, rel := e.bound.Resolved.Root.Path, e.bound.Resolved.ScopeRel
	if e.bound.Resolved.External {
		if e.catalog == nil || !e.catalog.Valid() {
			return "", sourcecatalog.Entry{}, fspath.FileObservation{}, false
		}
		rootPath, rel = e.catalog.RootPath, e.catalog.Rel
	}
	if rootPath == "" || rel == "" || protectedpath.IsCredentialFile(rel) || (e.bound.Resolved.ScopeRel != "" && protectedpath.IsCredentialFile(e.bound.Resolved.ScopeRel)) {
		return "", sourcecatalog.Entry{}, fspath.FileObservation{}, false
	}
	if inv != nil {
		if unsaved, err := sourceview.HoldsUnsavedText(inv.ctx, inv.tc, e.bound.Resolved); err != nil || unsaved {
			return "", sourcecatalog.Entry{}, fspath.FileObservation{}, false
		}
	}
	catEntry := sourcecatalog.Entry{Path: rel, Name: e.name, Size: e.stat.Size, Mode: uint32(e.stat.Mode), Modified: e.stat.ModTime.UTC()}
	return rootPath, catEntry, version, true
}

func (index *grepContentIndex) canPrune(inv *invocation, e entry) bool {
	index.stats.candidates++
	rootPath, catEntry, version, eligible := index.target(inv, e)
	if eligible {
		index.stats.eligible++
	}
	pruned := eligible && index.literals.CanPruneBytes(rootPath, index.require, catEntry, version)
	if pruned {
		index.stats.pruned++
	}
	return pruned
}

type contentSearchStats struct {
	candidates int
	eligible   int
	captured   int
	pruned     int
	learned    int
}

func (index *grepContentIndex) completed(inv *invocation) {
	slog.InfoContext(inv.ctx, "command content search completed",
		"execution_id", inv.request.Invocation.Identity.ExecutionID,
		"command", inv.spec.Name,
		"pruning_supported", !index.require.Empty(),
		"candidate_files", index.stats.candidates,
		"eligible_files", index.stats.eligible,
		"captured_files", index.stats.captured,
		"pruned_files", index.stats.pruned,
		"learned_files", index.stats.learned)
}

func contentVersion(e entry) fspath.FileObservation {
	return fspath.FileObservation{Identity: fspath.AllocationIdentity{Device: e.stat.Device, Inode: e.stat.Inode}, Changed: e.stat.ChangeTime.UnixNano(), Links: e.stat.Links, Size: e.stat.Size, Known: e.stat.POSIX && !e.stat.ChangeTime.IsZero()}
}
