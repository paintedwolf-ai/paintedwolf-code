package survey

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/pkg/api"
)

// WcTool counts lines, bytes, and optionally words in files.
type WcTool struct {
	Boundary *sandbox.Boundary
	Catalog  *sourcecatalog.Catalog
}

func (t *WcTool) Name() string { return "wc" }

type wcResult struct {
	Path  string `json:"path"`
	Lines *int   `json:"lines,omitempty"`
	Bytes int64  `json:"bytes"`
	Words *int   `json:"words,omitempty"`
	Files *int   `json:"files,omitempty"`
	// Truncated limits line and word counts to the scanned prefix.
	Truncated bool `json:"truncated,omitempty"`
	// InEditor marks counts taken over the person's unsaved editor text;
	// Bytes still reports the file on disk.
	InEditor bool `json:"in_editor,omitempty"`
}

type wcResponse struct {
	Results []wcResult `json:"results"`
	// Inventory is set when a recursive count read a generation that predates
	// the latest repository changes.
	Inventory *inventoryFacts `json:"inventory,omitempty"`
}

func (t *WcTool) Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ctx, _ = withInventoryReport(ctx)
	paths, err := toolkit.ParseStringSliceArg(args, "paths", safecmd.WCCaps().ResultCount)
	if err != nil {
		return "", err
	}
	includeWords := toolkit.BoolArg(args, "words", true)
	recursive := toolkit.BoolArg(args, "recursive", false)

	resp := wcResponse{Results: []wcResult{}}
	drafts := sourceview.DraftsFor(ctx, tctx)
	reads := projectpaths.NewReadSession(t.Boundary, tctx)
	defer reads.Close()
	for _, relPath := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		resolved, err := safecmd.ResolvePath(ctx, t.Boundary, tctx, relPath)
		if err != nil {
			return "", err
		}
		fullPath := resolved.Abs
		relSlash := filepath.ToSlash(resolved.DisplayPath)
		info, err := os.Stat(fullPath)
		if err != nil {
			return "", fmt.Errorf("wc %s: %w", relPath, err)
		}
		if info.IsDir() {
			if !recursive {
				return "", fmt.Errorf("wc %s: is a directory (set recursive: true to aggregate)", relPath)
			}
			entry, err := t.wcRecursiveDir(ctx, reads, tctx, resolved.Root, fullPath, relSlash, includeWords, drafts)
			if err != nil {
				return "", err
			}
			resp.Results = append(resp.Results, entry)
			continue
		}
		entry, err := wcFileEntry(ctx, reads, relSlash, fullPath, info, includeWords, drafts)
		if err != nil {
			return "", err
		}
		resp.Results = append(resp.Results, entry)
	}
	resp.Inventory = inventoryFactsFrom(ctx)
	root := "."
	if len(paths) == 1 {
		root = paths[0]
	}
	truncated := false
	for _, r := range resp.Results {
		if r.Truncated {
			truncated = true
			break
		}
	}
	for _, result := range resp.Results {
		kind := api.NavigationEntryKindFile
		if result.Files != nil {
			kind = api.NavigationEntryKindFolder
		}
		tctx.RecordSourcePath(result.Path, kind)
	}
	return safecmd.Shape(safecmd.ShapeInput{
		Tool:         "wc",
		Path:         root,
		PathsTouched: len(resp.Results),
		Truncated:    truncated,
		Value:        resp,
	})
}

// loadWcFile counts the person's unsaved editor text for a file they have
// open, and the file otherwise.
func loadWcFile(ctx context.Context, reads *projectpaths.ReadSession, drafts sourceview.DraftOverlay, fullPath string, info os.FileInfo) (content []byte, binary, bytesTruncated, fromEditor bool, err error) {
	if text, ok := drafts.Lookup(fullPath); ok {
		return text, false, false, true, nil
	}
	content, binary, bytesTruncated, err = readGrepFile(ctx, reads, fullPath, info)
	return content, binary, bytesTruncated, false, err
}

func wcFileEntry(ctx context.Context, reads *projectpaths.ReadSession, relSlash, fullPath string, info os.FileInfo, includeWords bool, drafts sourceview.DraftOverlay) (wcResult, error) {
	content, binary, bytesTruncated, fromEditor, err := loadWcFile(ctx, reads, drafts, fullPath, info)
	if err != nil {
		return wcResult{}, fmt.Errorf("wc %s: %w", relSlash, err)
	}
	entry := wcResult{
		// Bytes reports the on-disk size.
		Path:      relSlash,
		Bytes:     info.Size(),
		Truncated: bytesTruncated,
		InEditor:  fromEditor,
	}
	if !binary {
		text := string(content)
		lines := toolkit.CountLines(text)
		entry.Lines = &lines
		if includeWords {
			words := countWords(text)
			entry.Words = &words
		}
	}
	return entry, nil
}

func (t *WcTool) wcRecursiveDir(ctx context.Context, reads *projectpaths.ReadSession, tctx tools.ToolContext, root projectroot.RootRef, fullRoot, relRoot string, includeWords bool, drafts sourceview.DraftOverlay) (wcResult, error) {
	if strings.TrimSpace(tctx.WorkerBranchRoot) != "" {
		return t.wcRecursiveDirSurvey(ctx, reads, tctx, root, fullRoot, relRoot, includeWords)
	}
	var totals wcDirTotals
	inventory, err := sourceInventoryForScope(ctx, catalogOrProcess(t.Catalog), tctx.ProjectID, root, fullRoot)
	if err != nil {
		return wcResult{}, err
	}
	type wcJob struct {
		abs string
	}
	var jobs []wcJob
	err = inventory.walk(ctx, func(entry sourcecatalog.Entry) sourcecatalog.WalkStep {
		if entry.IsDir || entry.IsSymlink {
			return sourcecatalog.WalkContinue
		}
		abs := filepath.Join(root.Path, filepath.FromSlash(entry.Path))
		if _, err := reads.Resolve(ctx, abs); err != nil {
			return sourcecatalog.WalkContinue
		}
		jobs = append(jobs, wcJob{abs: abs})
		if len(jobs) >= safecmd.WCMaxRecursiveFiles {
			return sourcecatalog.WalkStop
		}
		return sourcecatalog.WalkContinue
	})
	if err != nil {
		return wcResult{}, err
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	if workers < 1 {
		workers = 1
	}

	results := make([]wcDirTotals, workers)
	var wg sync.WaitGroup
	chunkSize := (len(jobs) + workers - 1) / workers

	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(jobs) {
			end = len(jobs)
		}
		if start >= end {
			continue
		}
		wg.Add(1)
		go func(workerID, s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				if ctx.Err() != nil {
					return
				}
				job := jobs[i]
				info, statErr := os.Lstat(job.abs)
				if statErr != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					continue
				}
				content, binary, bytesTruncated, _, readErr := loadWcFile(ctx, reads, drafts, job.abs, info)
				if readErr != nil {
					continue
				}
				results[workerID].add(info.Size(), content, binary, bytesTruncated, includeWords)
			}
		}(w, start, end)
	}
	wg.Wait()
	for _, r := range results {
		totals.merge(r)
	}
	return totals.result(relRoot, includeWords), nil
}

func (t *WcTool) wcRecursiveDirSurvey(ctx context.Context, reads *projectpaths.ReadSession, tctx tools.ToolContext, root projectroot.RootRef, fullRoot, relRoot string, includeWords bool) (wcResult, error) {
	var totals wcDirTotals
	admit := func(_, abs string, isDir bool) bool {
		if isDir {
			return true
		}
		return t.Boundary == nil || t.Boundary.AssertReadScope(ctx, root.Path, projectroot.ScopeRel(root, abs), tctx.ProfileID()) == nil
	}
	walkErr := sandbox.SurveyWalk(ctx, fullRoot, sandbox.SurveyOptions{IncludeHidden: true, Admit: admit},
		func(e sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			if e.IsDir || e.IsSymlink {
				return sandbox.SurveyContinue, nil
			}
			if totals.files >= safecmd.WCMaxRecursiveFiles {
				return sandbox.SurveyStop, nil
			}
			info, err := e.DirEntry.Info()
			if err != nil {
				return sandbox.SurveyContinue, nil
			}
			content, binary, bytesTruncated, err := readGrepFile(ctx, reads, e.Abs, info)
			if err != nil {
				return sandbox.SurveyContinue, nil
			}
			totals.add(info.Size(), content, binary, bytesTruncated, includeWords)
			return sandbox.SurveyContinue, nil
		})
	if walkErr != nil {
		return wcResult{}, walkErr
	}
	return totals.result(relRoot, includeWords), nil
}

type wcDirTotals struct {
	bytes     int64
	lines     int
	words     int
	files     int
	truncated bool
	hasText   bool
}

func (t *wcDirTotals) merge(other wcDirTotals) {
	t.files += other.files
	t.bytes += other.bytes
	t.lines += other.lines
	t.words += other.words
	if other.truncated {
		t.truncated = true
	}
	if other.hasText {
		t.hasText = true
	}
}

func (t *wcDirTotals) add(size int64, content []byte, binary, bytesTruncated, includeWords bool) {
	t.files++
	t.bytes += size
	if bytesTruncated {
		t.truncated = true
	}
	if binary {
		return
	}
	text := string(content)
	t.lines += toolkit.CountLines(text)
	if includeWords {
		t.words += countWords(text)
	}
	t.hasText = true
}

func (t wcDirTotals) result(relRoot string, includeWords bool) wcResult {
	entry := wcResult{
		Path:      relRoot,
		Bytes:     t.bytes,
		Files:     &t.files,
		Truncated: t.truncated,
	}
	if t.hasText {
		entry.Lines = &t.lines
		if includeWords {
			entry.Words = &t.words
		}
	}
	return entry
}

func countWords(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return len(strings.Fields(text))
}
