package survey

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tsparse"
	"golang.org/x/mod/modfile"
)

type summarizeGatherer struct {
	access    *summaryAccess
	caps      summarize.Caps
	rerank    decide.Reranker
	sources   *summarySources
	trees     *summaryTrees
	relations *summaryRelations
	patterns  *summaryPatterns
	nested    *summaryNestedRepos
}

// Source detail shares one read budget and memo for the complete tool call.
type summarySources struct {
	access            *summaryAccess
	caps              summarize.Caps
	rerank            decide.Reranker
	task              string
	moduleImport      func(context.Context, string) string
	parseFailures     []tsparse.FileFailure
	parseFailureCount int
	skippedPaths      map[string]bool
	nonTextPaths      map[string]bool
	truncatedPaths    map[string]bool
	readReservations  map[string]int64
	sourceFiles       int
	sourceBytes       int64
	sourceReadBytes   int64
	readMu            sync.Mutex
	sourceLimited     bool
	memoMu            sync.Mutex
	structureByPath   map[string]structureCacheEntry
	bytesByAbs        map[string][]byte
}

// Tree readers and deferred child pages live until the pack has been serialized.
type summaryTrees struct {
	access            *summaryAccess
	sources           *summarySources
	nested            *summaryNestedRepos
	caps              summarize.Caps
	documentLeads     func(context.Context, string, int) []string
	treeViews         map[string]summaryTreeLookup
	treeReaders       []*sourcecatalog.SummaryReader
	treeErr           error
	treeRequest       summarize.Request
	treeState         sourcecatalog.State
	treeRefreshing    bool
	treeNodes         int
	directoryEntries  int
	directoriesOpened int
	indexWaitUsed     bool
	memoMu            sync.Mutex
	subtreeByPath     map[string]*summarize.SubtreeNode
	catalogRevision   uint64
}

type summaryRelations struct {
	access           *summaryAccess
	sources          *summarySources
	nested           *summaryNestedRepos
	caps             summarize.Caps
	faninGrepPasses  *int
	memoMu           sync.Mutex
	goImportByPkgDir map[string]string
	goModByAbs       map[string]*modfile.File
}

type summaryPatterns struct {
	access             *summaryAccess
	sources            *summarySources
	trees              *summaryTrees
	caps               summarize.Caps
	patternFilesTotal  int
	patternNextPath    string
	patternCursorFound bool
}

type summaryNestedRepos struct {
	enabled          bool
	memoMu           sync.Mutex
	nestedPruneCount *int
	nestedPruneSeen  map[string]struct{}
}

func newSummarizeGatherer(boundary *sandbox.Boundary, reads *projectpaths.ReadSession, caps summarize.Caps, tctx tools.ToolContext, catalog *sourcecatalog.Catalog, rerank decide.Reranker) *summarizeGatherer {
	access := newSummaryAccess(boundary, reads, tctx, catalog)
	nested := &summaryNestedRepos{enabled: caps.Gather.PruneNestedVCS}
	sources := &summarySources{access: access, caps: caps, rerank: rerank}
	relations := &summaryRelations{access: access, sources: sources, nested: nested, caps: caps}
	sources.moduleImport = relations.goPackageImportPath
	trees := &summaryTrees{access: access, sources: sources, nested: nested, caps: caps, documentLeads: relations.seedDocLinkTargets}
	patterns := &summaryPatterns{access: access, sources: sources, trees: trees, caps: caps}
	return &summarizeGatherer{access: access, caps: caps, rerank: rerank, sources: sources, trees: trees, relations: relations, patterns: patterns, nested: nested}
}
