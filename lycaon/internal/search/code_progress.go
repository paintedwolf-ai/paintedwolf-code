package search

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// CodeProgress is a bounded frontier, not a retained reader or a query job.
// Its caller binds it to the query and root set, and serializes access.
type CodeProgress struct {
	Root      int
	Selection int
	After     string
	Revision  uint64
	Instance  uint64
	Epoch     repochange.Epoch
}

func (e *CodeExecutor) discoverCandidates(ctx context.Context, leg *CodePlanLeg, paths pathGlobFilter, spec codeScanSpec) (ExecutorReport, error) {
	progress := leg.Progress
	if progress == nil {
		progress = &CodeProgress{}
	}
	scanCtx, cancel := context.WithTimeout(ctx, e.legWallBudget(leg))
	defer cancel()
	report := ExecutorReport{}
	// Each candidate nominates a whole file, so a line cap cannot strand a
	// continuation in the middle of a file full of references.
	spec.lineCap = 1
	for progress.Root < len(leg.PathRoots) {
		root := leg.PathRoots[progress.Root]
		selections := codeIndexSelections(scanCtx, e.catalog, root, leg.Query, leg.Flags, leg.IncludeDependencies)
		if progress.Selection >= len(selections) {
			progress.Root++
			progress.Selection = 0
			continue
		}
		selection := selections[progress.Selection]
		gen, err := resolveRequestedCodeGeneration(scanCtx, e.catalog, root, e.generationWaitBudget(), selection.include, selection.paths...)
		if errors.Is(err, errCodeCatalogWarming) {
			report.Code.WarmingRoots++
			break
		}
		if err != nil {
			return report, err
		}
		done, err := e.candidatePageScan(scanCtx, gen, paths.withDependencies(scanCtx, e.catalog, root.Path, leg.IncludeDependencies), spec, leg.Cap, progress, &report)
		_ = gen.reader.Close()
		if scanCtx.Err() != nil {
			report.TimedOut = true
			break
		}
		if err != nil {
			return report, err
		}
		if !done {
			break
		}
		progress.Selection++
		progress.After, progress.Revision, progress.Instance, progress.Epoch = "", 0, 0, repochange.Epoch{}
	}
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	return report, nil
}

func (e *CodeExecutor) candidatePageScan(ctx context.Context, gen codeGeneration, paths pathGlobFilter, spec codeScanSpec, cap int, progress *CodeProgress, report *ExecutorReport) (bool, error) {
	epoch := repochange.CurrentEpoch(gen.rootPath)
	if progress.Revision != 0 && (progress.Instance != gen.reader.Status.Instance || progress.Revision != gen.reader.Status.Revision || progress.Epoch != epoch) {
		// The caller invalidates confirmed declarations as well. Do not splice
		// generations if publication raced the caller's initial snapshot.
		report.Issues = append(report.Issues, Issue{Executor: ExecutorCode, Reason: IssueCatalogRefreshing})
		return false, nil
	}
	progress.Revision, progress.Instance, progress.Epoch = gen.reader.Status.Revision, gen.reader.Status.Instance, epoch
	coverage, err := gen.reader.Coverage(ctx)
	if err != nil {
		return false, err
	}
	report.Code.observeCoverage(coverage)
	for ctx.Err() == nil {
		entries, err := gen.reader.FilePage(ctx, paths.codeScope(), progress.After, sourcecatalog.TreeFilePageLimit)
		if err != nil {
			return false, err
		}
		if len(entries) == 0 {
			return coverage.DiscoveryComplete && !coverage.Refreshing, nil
		}
		files := codeFilesFromEntries(gen.root, gen.rootID, gen.rootPath, entries)
		jobs, warming := e.contentJobs(ctx, gen, paths.codeScope(), entries, files, spec, &report.Code)
		candidates := make(map[string]codeScanJob, len(jobs))
		for _, job := range jobs {
			candidates[job.file.rel] = job
		}
		if warming {
			report.Code.IndexWarmingRoots = 1
		}
		for _, entry := range entries {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if dir, excluded := dependencyDirOf(entry.Path, spec.lineExcludes); excluded {
				progress.After = dir + "/\xff"
				break
			}
			if job, candidate := candidates[entry.Path]; candidate && paths.allowsCode(entry.Path) {
				outcome := scanOneCodeFile(ctx, job, 0, spec)
				if ctx.Err() != nil {
					return false, ctx.Err()
				}
				report.Hits = append(report.Hits, outcome.lineHits...)
				if outcome.skipped {
					report.SkippedFiles++
				}
				if outcome.opened {
					report.Code.FilesOpened++
				}
				if outcome.prefiltered {
					report.Code.PrefilterSkipped++
				}
			}
			report.Code.FilesListed++
			progress.After = entry.Path
			if len(report.Hits) >= cap {
				report.Limited = true
				return false, nil
			}
		}
	}
	return false, ctx.Err()
}
