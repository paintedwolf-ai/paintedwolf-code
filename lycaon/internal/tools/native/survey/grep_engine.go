package survey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func grepWorkerCount() int {
	n := runtime.GOMAXPROCS(0)
	if n <= 2 {
		return 1
	}
	return min(max(n/2, 2), grepWorkerMax)
}

type grepFileJob struct {
	abs string
	rel string
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

func (t *GrepTool) grepWalkTree(ctx context.Context, tctx tools.ToolContext, target grepTarget, includeHidden bool, search *grepSearch, walkExtra sandbox.SurveyOptions) error {
	search.publishProgress("searching")
	if search.structural || search.offset > 0 {
		return t.grepWalkTreeSequential(ctx, tctx, target, includeHidden, search, walkExtra)
	}
	return t.grepWalkTreeParallel(ctx, tctx, target, includeHidden, search, walkExtra)
}

func (t *GrepTool) grepWalkTreeSequential(ctx context.Context, tctx tools.ToolContext, target grepTarget, includeHidden bool, search *grepSearch, walkExtra sandbox.SurveyOptions) error {
	if strings.TrimSpace(tctx.WorkerBranchRoot) != "" {
		return t.grepWalkTreeSequentialSurvey(ctx, tctx, target, includeHidden, search, walkExtra)
	}
	var scanErr error
	err := t.forEachGrepCatalogJob(ctx, tctx, target, includeHidden, walkExtra, search, func(job grepFileJob) bool {
		if scanErr = ctx.Err(); scanErr != nil {
			return false
		}
		if !search.pathFilter.Match(job.rel) {
			return true
		}
		info, statErr := os.Lstat(job.abs)
		if statErr != nil || !info.Mode().IsRegular() {
			search.unreadable++
			return true
		}
		content, binary, bytesTruncated, readErr := search.loadGrepFile(ctx, job.abs, info)
		if readErr != nil {
			search.unreadable++
			return true
		}
		if binary {
			if search.stats != nil {
				search.stats.BinarySkipped.Add(1)
				search.stats.FilesOpened.Add(1)
			}
			scanErr = search.applyFileOutcome(grepFileOutcome{binarySkipped: true})
		} else {
			if search.stats != nil {
				search.stats.FilesOpened.Add(1)
			}
			scanErr = search.scanFile(ctx, job.rel, content, bytesTruncated)
		}
		search.publishProgress("searching")
		return scanErr == nil
	})
	if err != nil {
		return err
	}
	if errors.Is(scanErr, errGrepMatchCap) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return scanErr
}

func (t *GrepTool) grepWalkTreeSequentialSurvey(ctx context.Context, tctx tools.ToolContext, target grepTarget, includeHidden bool, search *grepSearch, walkExtra sandbox.SurveyOptions) error {
	opts, err := t.grepSurveyOptions(ctx, tctx, target, includeHidden, walkExtra)
	if err != nil {
		return err
	}
	return workerBranchOrSurveyWalk(ctx, tctx, target.fullRoot, opts,
		func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if e.IsDir {
				return sandbox.SurveyContinue, nil
			}
			rel := projectpaths.QualifyAbs(tctx, target.root, e.Abs)
			if !search.pathFilter.Match(rel) {
				return sandbox.SurveyContinue, nil
			}
			if err := ensureBranchFileForRead(ctx, tctx, e.Abs); err != nil {
				search.unreadable++
				return sandbox.SurveyContinue, nil
			}
			info, err := e.DirEntry.Info()
			if err != nil {
				search.unreadable++
				return sandbox.SurveyContinue, nil
			}
			content, binary, bytesTruncated, err := search.loadGrepFile(ctx, e.Abs, info)
			if err != nil {
				if ctx.Err() != nil {
					return sandbox.SurveyStop, ctx.Err()
				}
				search.unreadable++
				return sandbox.SurveyContinue, nil
			}
			var scanErr error
			if binary {
				if search.stats != nil {
					search.stats.BinarySkipped.Add(1)
					search.stats.FilesOpened.Add(1)
				}
				scanErr = search.applyFileOutcome(grepFileOutcome{binarySkipped: true})
			} else {
				if search.stats != nil {
					search.stats.FilesOpened.Add(1)
				}
				scanErr = search.scanFile(ctx, rel, content, bytesTruncated)
			}
			if scanErr != nil {
				if errors.Is(scanErr, errGrepMatchCap) {
					return sandbox.SurveyStop, nil
				}
				return sandbox.SurveyContinue, scanErr
			}
			search.publishProgress("searching")
			return sandbox.SurveyContinue, nil
		})
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

func (t *GrepTool) grepWalkTreeParallel(ctx context.Context, tctx tools.ToolContext, target grepTarget, includeHidden bool, search *grepSearch, walkExtra sandbox.SurveyOptions) error {
	search.ctx, search.boundary, search.tctx = ctx, t.Boundary, tctx
	stream := search.startStream(grepWorkerCount())
	var walkErr error
	if strings.TrimSpace(tctx.WorkerBranchRoot) == "" {
		walkErr = t.forEachGrepCatalogJob(ctx, tctx, target, includeHidden, walkExtra, search, stream.submit)
	} else {
		walkErr = t.forEachGrepBranchJob(ctx, tctx, target, includeHidden, walkExtra, stream.submit)
	}
	scanErr := stream.finish()
	if walkErr != nil {
		return walkErr
	}
	if errors.Is(scanErr, errGrepMatchCap) {
		return nil
	}
	if scanErr != nil {
		return scanErr
	}
	return ctx.Err()
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

func (s *grepSearch) startStream(workers int) *grepStream {
	ctx, cancel := context.WithCancel(s.ctx)
	window := workers * 4
	st := &grepStream{
		search: s, ctx: ctx, cancel: cancel,
		jobs: make(chan grepStreamJob, window), pending: make(chan chan grepFileOutcome, window),
		applied: make(chan struct{}),
	}
	for range workers {
		st.workers.Add(1)
		go st.work()
	}
	go st.apply()
	return st
}

func (st *grepStream) work() {
	defer st.workers.Done()
	for task := range st.jobs {
		if err := st.ctx.Err(); err != nil {
			task.out <- grepFileOutcome{err: err}
			continue
		}
		task.out <- st.search.searchAbsFile(st.ctx, task.job)
	}
}

// apply consumes outcomes in submission order. After the first error it keeps
// draining so the walk never blocks on a full window.
func (st *grepStream) apply() {
	defer close(st.applied)
	for out := range st.pending {
		if st.err != nil {
			continue
		}
		outcome := <-out
		if err := st.search.applyFileOutcome(outcome); err != nil {
			st.err = err
			st.stopped.Store(true)
			st.cancel()
			continue
		}
		st.search.publishProgress("searching")
	}
}

// submit queues one file in walk order; false stops the walk.
func (st *grepStream) submit(job grepFileJob) bool {
	if st.stopped.Load() || st.ctx.Err() != nil {
		return false
	}
	if !st.search.pathFilter.Match(job.rel) {
		return true
	}
	out := make(chan grepFileOutcome, 1)
	select {
	case st.pending <- out:
	case <-st.ctx.Done():
		return false
	}
	st.jobs <- grepStreamJob{job: job, out: out}
	return true
}

// finish waits for every queued file and returns the first applied error.
func (st *grepStream) finish() error {
	close(st.jobs)
	close(st.pending)
	st.workers.Wait()
	<-st.applied
	st.cancel()
	return st.err
}

func (t *GrepTool) forEachGrepBranchJob(ctx context.Context, tctx tools.ToolContext, target grepTarget, includeHidden bool, walkExtra sandbox.SurveyOptions, yield func(grepFileJob) bool) error {
	opts, err := t.grepSurveyOptions(ctx, tctx, target, includeHidden, walkExtra)
	if err != nil {
		return err
	}
	return workerBranchOrSurveyWalk(ctx, tctx, target.fullRoot, opts,
		func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if e.IsDir {
				return sandbox.SurveyContinue, nil
			}
			if err := ctx.Err(); err != nil {
				return sandbox.SurveyStop, err
			}
			if !yield(grepFileJob{abs: e.Abs, rel: projectpaths.QualifyAbs(tctx, target.root, e.Abs)}) {
				return sandbox.SurveyStop, nil
			}
			return sandbox.SurveyContinue, nil
		})
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
	inventory, err := sourceInventoryForScope(ctx, t.Catalog, tctx.ProjectID, target.root, target.fullRoot)
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
			if _, hasDraft := search.drafts.Lookup(abs); !hasDraft && t.Catalog.CanPrune(target.root.Path, search.require, entry) {
				if search.stats != nil {
					search.stats.IndexBloomPruned.Add(1)
				}
				return sourcecatalog.WalkContinue
			}
		}
		if !yield(grepFileJob{abs: abs, rel: projectpaths.QualifyAbs(tctx, target.root, abs)}) {
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

func (s *grepSearch) searchAbsFile(ctx context.Context, job grepFileJob) grepFileOutcome {
	out := grepFileOutcome{}
	if !s.pathFilter.Match(job.rel) {
		out.globSkipped = true
		return out
	}
	if err := ensureBranchFileForRead(ctx, s.tctx, job.abs); err != nil {
		out.unreadable = true
		return out
	}
	info, err := os.Lstat(job.abs)
	if err != nil || !info.Mode().IsRegular() {
		out.unreadable = true
		return out
	}
	if s.stats != nil {
		s.stats.FilesOpened.Add(1)
	}
	content, binary, bytesTruncated, err := s.loadGrepFile(ctx, job.abs, info)
	if err != nil {
		out.unreadable = true
		return out
	}
	if binary {
		if s.stats != nil {
			s.stats.BinarySkipped.Add(1)
		}
		out.binarySkipped = true
		return out
	}
	scanned := s.scanContent(job.rel, content)
	scanned.bytesTruncated = bytesTruncated
	return scanned
}
