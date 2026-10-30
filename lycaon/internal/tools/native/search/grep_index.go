package search

import (
	"log/slog"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// grepContentIndex owns pruning and captured-byte observation for one search.
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

func contentEntry(e entry) sourcecatalog.Entry {
	return sourcecatalog.Entry{Path: e.bound.Resolved.ScopeRel, Name: e.name, Size: e.stat.Size, Mode: uint32(e.stat.Mode), Modified: e.stat.ModTime.UTC()}
}

func (index grepContentIndex) eligible(e entry) bool {
	if index.literals == nil || e.link || e.bound.Resolved.External || e.bound.Resolved.Compressed || e.stat.Links == 0 || !e.stat.POSIX {
		return false
	}
	return true
}

func (index *grepContentIndex) canPrune(e entry) bool {
	index.stats.candidates++
	eligible := index.eligible(e)
	if eligible {
		index.stats.eligible++
	}
	pruned := e.depth > 0 && eligible && index.literals.CanPruneBytes(e.bound.Resolved.Root.Path, index.require, contentEntry(e), contentVersion(e))
	if pruned {
		index.stats.pruned++
	}
	return pruned
}

func (index grepContentIndex) begin(e entry) *sourcecatalog.ContentRead {
	if !index.eligible(e) {
		return nil
	}
	return index.literals.BeginRead(e.bound.Resolved.Root.Path, contentEntry(e), contentVersion(e))
}

type contentSearchStats struct {
	candidates int
	eligible   int
	captured   int
	pruned     int
	learned    int
}

func (index *grepContentIndex) captured(read *sourcecatalog.ContentRead) {
	defer read.Close()
	if read.Learned() {
		index.stats.learned++
	}
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
