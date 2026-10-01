package search

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

type replaceFileOutcome struct {
	seq     int
	preview *ReplaceFilePreview
	skipped bool
}

// PreviewReplace builds guarded hunks and skips binary or oversized files.
func PreviewReplace(ctx context.Context, req ReplacePreviewRequest) (ReplacePreviewResult, error) {
	return previewReplace(ctx, sourcecatalog.Process(), req)
}

func previewReplace(ctx context.Context, catalog *sourcecatalog.Catalog, req ReplacePreviewRequest) (ReplacePreviewResult, error) {
	pattern, err := replacementPattern(req.Query)
	if err != nil {
		return ReplacePreviewResult{}, err
	}
	matcher, err := compileReplaceMatcher(pattern, req.Flags)
	if err != nil {
		return ReplacePreviewResult{}, ensureMatchError(err)
	}
	paths, err := compileCodePaths(req.Query, req.Flags)
	if err != nil {
		return ReplacePreviewResult{}, err
	}
	prefilter := compileReplacePrefilter(pattern, req.Flags)
	excludes := dependencyDirSet(req.ExcludeDirs)
	result := ReplacePreviewResult{State: ReplacePreviewReady, Files: []ReplaceFilePreview{}, Issues: []Issue{}}
	report := ExecutorReport{}
	totalHunk := 0
	for _, root := range req.Roots {
		if strings.TrimSpace(root.Path) == "" {
			continue
		}
		if len(result.Files) >= ReplaceMaxFiles || totalHunk >= ReplaceMaxHunks {
			result.Truncated = true
			break
		}
		gen, genErr := resolveCodeGeneration(ctx, catalog, root, codeGenerationJoinGrace)
		if errors.Is(genErr, errCodeCatalogWarming) {
			report.Code.WarmingRoots++
			continue
		}
		if genErr != nil {
			if ctx.Err() != nil {
				return ReplacePreviewResult{}, ctx.Err()
			}
			report.Issues = append(report.Issues, Issue{Executor: ExecutorCode, Reason: IssueExecutorError, Message: genErr.Error()})
			continue
		}
		coverage, coverageErr := gen.reader.Coverage(ctx)
		if coverageErr != nil {
			_ = gen.reader.Close()
			return ReplacePreviewResult{}, coverageErr
		}
		report.Code.observeCoverage(coverage)
		if coverage.Pending() {
			_ = gen.reader.Close()
			continue
		}
		part, previewErr := previewReplaceGeneration(ctx, gen, req, paths, excludes, matcher, prefilter, ReplaceMaxFiles-len(result.Files), ReplaceMaxHunks-totalHunk)
		if previewErr != nil {
			return ReplacePreviewResult{}, previewErr
		}
		result.Files = append(result.Files, part.files...)
		for _, file := range part.files {
			totalHunk += len(file.Hunks)
		}
		report.SkippedFiles += part.skipped
		result.Truncated = result.Truncated || part.truncated
	}
	if err := ctx.Err(); err != nil {
		return ReplacePreviewResult{}, err
	}
	result.Issues = append(result.Issues, report.CoverageIssues(ExecutorCode)...)
	if result.Truncated {
		result.Issues = append(result.Issues, Issue{Executor: ExecutorCode, Reason: IssueResultLimit})
	}
	if len(result.Issues) > 0 {
		result.State = ReplacePreviewLimited
	}
	if report.Code.WarmingRoots > 0 || report.Code.IncompleteRoots > 0 || report.Code.RefreshingRoots > 0 {
		result.State = ReplacePreviewPreparing
		result.Files = []ReplaceFilePreview{}
	}
	return result, nil
}

type replacePreviewPage struct {
	files     []ReplaceFilePreview
	truncated bool
	skipped   int
}

func previewReplaceGeneration(ctx context.Context, gen codeGeneration, req ReplacePreviewRequest, paths pathGlobFilter, excludes dependencyDirs, matcher replaceMatcher, prefilter codePrefilter, fileRoom, hunkRoom int) (replacePreviewPage, error) {
	defer func() { _ = gen.reader.Close() }()
	result := replacePreviewPage{}
	err := visitCodePages(ctx, gen, paths, excludes, func(_ []sourcecatalog.Entry, page []codeFile) bool {
		jobs := page[:0]
		for _, file := range page {
			if replacementPathAllowed(req.Query, file.rel) {
				jobs = append(jobs, file)
			}
		}
		part := previewReplaceFiles(ctx, jobs, matcher, prefilter, req.Replacement, fileRoom, hunkRoom)
		result.files = append(result.files, part.files...)
		result.skipped += part.skipped
		fileRoom -= len(part.files)
		for _, file := range part.files {
			hunkRoom -= len(file.Hunks)
		}
		result.truncated = part.truncated || fileRoom <= 0 || hunkRoom <= 0
		return !result.truncated
	})
	return result, err
}

func previewReplaceFiles(
	ctx context.Context,
	files []codeFile,
	matcher replaceMatcher,
	prefilter codePrefilter,
	replacement string,
	fileRoom, hunkRoom int,
) replacePreviewPage {
	if fileRoom <= 0 || hunkRoom <= 0 {
		return replacePreviewPage{truncated: true}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	workers := codeWorkerCount()
	jobs := make(chan int, workers*2)
	outcomes := make(chan replaceFileOutcome, workers*2)
	// Credits bound completed previews waiting behind a slow earlier file.
	credits := make(chan struct{}, workers*2)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					outcomes <- replaceFileOutcome{seq: i}
					continue
				}
				outcomes <- previewOneReplaceFile(ctx, files[i], i, matcher, prefilter, replacement)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range files {
			select {
			case credits <- struct{}{}:
			case <-ctx.Done():
				return
			}
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(outcomes)
	}()

	result := replacePreviewPage{}
	totalHunk := 0
	next := 0
	pending := map[int]replaceFileOutcome{}
	for oc := range outcomes {
		pending[oc.seq] = oc
		for {
			o, have := pending[next]
			if !have {
				break
			}
			delete(pending, next)
			next++
			<-credits
			if o.skipped {
				result.skipped++
			}
			if o.preview == nil {
				continue
			}
			hunks := o.preview.Hunks
			room := hunkRoom - totalHunk
			if len(hunks) > room {
				hunks = hunks[:room]
				o.preview.Hunks = hunks
				result.truncated = true
			}
			result.files = append(result.files, *o.preview)
			totalHunk += len(hunks)
			if len(result.files) >= fileRoom || totalHunk >= hunkRoom {
				cancel()
				for range outcomes {
				}
				result.truncated = true
				return result
			}
		}
	}
	return result
}

func previewOneReplaceFile(ctx context.Context,
	file codeFile,
	seq int,
	matcher replaceMatcher,
	prefilter codePrefilter,
	replacement string,
) replaceFileOutcome {
	out := replaceFileOutcome{seq: seq}
	doc, status := openCodeDocument(file.abs, replaceReadMaxBytes)
	if status != codeOpenOK {
		out.skipped = status != codeOpenBinary
		return out
	}
	text := doc.Text()
	if prefilter.active() && !prefilter.matches([]byte(text)) {
		return out
	}
	hunks := collectReplaceHunks(text, matcher, replacement)
	if len(hunks) == 0 {
		return out
	}
	classifyReplaceContexts(ctx, file.rel, text, hunks)
	out.preview = &ReplaceFilePreview{
		RootID: file.rootID,
		Path:   file.rel,
		SHA256: doc.RawSHA256(),
		Hunks:  hunks,
	}
	return out
}
