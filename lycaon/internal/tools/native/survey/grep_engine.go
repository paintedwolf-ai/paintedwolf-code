package survey

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// grepReadCap bounds concurrent file reads across every search in the process.
const grepReadCap = 4

// grepWorkerMax bounds one search's workers. Matching runs outside the read
// cap, so workers beyond it keep matching while reads wait.
const grepWorkerMax = 8

// grepEngineStats counts engine decisions.
type grepEngineStats struct {
	FilesOpened      atomic.Int64
	BinarySkipped    atomic.Int64
	PrefilterSkipped atomic.Int64
	PrefilterRan     atomic.Int64
	RegexOrScanRuns  atomic.Int64
	IndexBloomPruned atomic.Int64
}

type grepFileJob struct {
	abs string
	rel string
	// Structured identity of the file under its project root.
	rootID   string
	scopeRel string
}

// grepFileIdentity carries the resolved {RootID, ScopeRel} of one scanned file
// so matches record source identity without re-resolving display strings.
type grepFileIdentity struct {
	rootID   string
	scopeRel string
}

type grepFileOutcome struct {
	rel     string
	matches []grepMatch
	// globSkipped excludes the file from searched counts.
	globSkipped      bool
	binarySkipped    bool
	prefilterSkipped bool
	bytesTruncated   bool
	unreadable       bool
	err              error
}

func (t *GrepTool) grepSurveyOptions(
	ctx context.Context,
	tctx tools.ToolContext,
	target grepTarget,
	includeHidden bool,
	walkExtra sandbox.SurveyOptions,
) (sandbox.SurveyOptions, error) {
	readFilter, err := t.Boundary.CompileReadFilter(ctx, target.root.Path, tctx.ProfileID())
	if err != nil {
		return sandbox.SurveyOptions{}, err
	}
	admit := func(_, abs string, isDir bool) bool {
		return readFilter == nil || readFilter(projectroot.ScopeRel(target.root, abs), isDir)
	}
	opts := sandbox.SurveyOptions{
		IncludeHidden: includeHidden, Admit: admit, Scope: walkExtra.Scope,
		PruneNestedVCS: walkExtra.PruneNestedVCS, OnNestedRepoPruned: walkExtra.OnNestedRepoPruned,
	}
	return opts, nil
}

// grepStream searches files on a worker pool while the walk is still
// producing them, and applies outcomes in walk order so results and match
// caps stay deterministic. At most window files are in flight or awaiting
// their turn, which bounds the memory held by finished outcomes.
type grepStream struct {
	search  *grepSearch
	ctx     context.Context
	cancel  context.CancelFunc
	jobs    chan grepStreamJob
	pending chan chan grepFileOutcome
	workers sync.WaitGroup
	applied chan struct{}
	stopped atomic.Bool
	err     error
}

type grepStreamJob struct {
	job grepFileJob
	out chan grepFileOutcome
}

func (t *GrepTool) forEachGrepCatalogJob(
	ctx context.Context,
	tctx tools.ToolContext,
	target grepTarget,
	includeHidden bool,
	walkExtra sandbox.SurveyOptions,
	search *grepSearch,
	yield func(grepFileJob) bool,
) error {
	inventory, err := sourceInventoryForScope(ctx, catalogOrProcess(t.Catalog), tctx.Identity.ProjectID, target.root, target.fullRoot)
	if err != nil {
		return err
	}
	readFilter, err := t.Boundary.CompileReadFilter(ctx, target.root.Path, tctx.ProfileID())
	if err != nil {
		return err
	}
	canPrune := search != nil && !search.structural && !search.require.Empty() && t.Catalog != nil
	return inventory.walk(ctx, func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
		if !includeHidden && strings.HasPrefix(entry.Name, ".") {
			return sourcecatalog.WalkSkip
		}
		abs := filepath.Join(target.root.Path, filepath.FromSlash(entry.Path))
		walkRel := catalogRelativePath(entry.Path, inventory.base)
		if walkExtra.Admit != nil && !walkExtra.Admit(walkRel, abs, entry.IsDir) {
			return sourcecatalog.WalkSkip
		}
		if !admitsCatalogEntry(walkExtra.Scope, walkRel, entry.IsDir) {
			return sourcecatalog.WalkSkip
		}
		if readFilter != nil && !readFilter(entry.Path, entry.IsDir) {
			return sourcecatalog.WalkSkip
		}
		if entry.IsDir {
			if walkExtra.PruneNestedVCS && entry.IsVCSRoot {
				if walkExtra.OnNestedRepoPruned != nil {
					walkExtra.OnNestedRepoPruned(abs)
				}
				return sourcecatalog.WalkSkip
			}
			return sourcecatalog.WalkContinue
		}
		if entry.IsSymlink {
			return sourcecatalog.WalkContinue
		}
		if canPrune {
			if _, hasDraft := search.drafts.Lookup(abs); !hasDraft && t.Catalog.Literals.CanPrune(target.root.Path, search.require, entry) {
				if search.stats != nil {
					search.stats.IndexBloomPruned.Add(1)
				}
				return sourcecatalog.WalkContinue
			}
		}
		if !yield(grepFileJob{abs: abs, rel: projectpaths.QualifyAbs(tctx, target.root, abs), rootID: target.root.ID, scopeRel: projectroot.ScopeRel(target.root, abs)}) {
			return sourcecatalog.WalkStop
		}
		return sourcecatalog.WalkContinue
	})
}

func (s *grepSearch) applyFileOutcome(out grepFileOutcome) error {
	if out.err != nil {
		return out.err
	}
	if out.globSkipped {
		return nil
	}
	if out.unreadable {
		s.unreadable++
		return nil
	}
	if out.bytesTruncated {
		s.filesBytesTruncated++
	}
	if out.binarySkipped {
		s.binarySkipped++
		return nil
	}
	if out.prefilterSkipped {
		s.prefilterSkipped++
		return nil
	}
	s.textScanned++
	if out.rel != "" {
		s.noteSubtree(out.rel)
	}
	for _, entry := range out.matches {
		if err := s.acceptMatch(entry); err != nil {
			return err
		}
	}
	return nil
}

func (s *grepSearch) acceptMatch(entry grepMatch) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.skipped < s.offset {
		s.skipped++
		return nil
	}
	if s.matchSink != nil {
		s.matchSink(entry)
		return nil
	}
	s.resp.Matches = append(s.resp.Matches, entry)
	if len(s.resp.Matches) >= s.maxMatches {
		s.resp.Truncated = true
		return errGrepMatchCap
	}
	return nil
}
