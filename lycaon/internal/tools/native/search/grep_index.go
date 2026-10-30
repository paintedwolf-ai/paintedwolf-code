package search

import (
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// grepContentIndex owns pruning and captured-byte observation for one search.
type grepContentIndex struct {
	literals *sourcecatalog.LiteralSearch
	require  litprefilter.Requirement
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
	if index.literals == nil || e.link || e.bound.Resolved.External || e.bound.Resolved.Compressed {
		return false
	}
	return true
}

func (index grepContentIndex) canPrune(e entry) bool {
	return e.depth > 0 && index.eligible(e) && index.literals.CanPruneBytes(e.bound.Resolved.Root.Path, index.require, contentEntry(e))
}

func (index grepContentIndex) begin(e entry) *sourcecatalog.ContentRead {
	if !index.eligible(e) {
		return nil
	}
	return index.literals.BeginRead(e.bound.Resolved.Root.Path, contentEntry(e))
}
