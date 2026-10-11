package search

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/litprefilter"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
)

// codeFile is one regular file the code engine may read.
type codeFile struct {
	root   CodeRoot
	rootID string
	rel    string
	abs    string
}

// codeGeneration pins a disk-backed metadata view for one search root.
type codeGeneration struct {
	root      CodeRoot
	rootID    string
	rootPath  string
	reader    *sourcecatalog.IndexReader
	ephemeral bool
}

func resolveCodeGeneration(ctx context.Context, catalog *sourcecatalog.Catalog, root CodeRoot, wait time.Duration) (codeGeneration, error) {
	rootPath := strings.TrimSpace(root.Path)
	rootID := strings.TrimSpace(root.RootID)
	if rootID == "" {
		return codeGeneration{}, errors.New("code root ID is required")
	}
	catalogRoot := sourcecatalog.Root{ID: rootID, Path: rootPath}
	reader, status, err := catalog.Trees.OpenIndex(ctx, root.ProjectID, catalogRoot, 0)
	if err == nil && reader == nil && wait > 0 {
		reader, status, err = catalog.Trees.OpenIndex(ctx, root.ProjectID, catalogRoot, wait)
	}
	if err != nil {
		return codeGeneration{}, err
	}
	if reader == nil {
		if status.State == sourcecatalog.StateFailed {
			return codeGeneration{}, fmt.Errorf("source catalog: %s", status.Error)
		}
		return codeGeneration{}, errCodeCatalogWarming
	}
	return codeGeneration{root: root, rootID: rootID, rootPath: rootPath, reader: reader}, nil
}

var errCodeCatalogWarming = errors.New("source catalog is warming")

// codeFilesFromEntries addresses each indexed file on disk; the projection
// returns only regular files.
func codeFilesFromEntries(root CodeRoot, rootID, rootPath string, entries []sourcecatalog.Entry) []codeFile {
	out := make([]codeFile, 0, len(entries))
	for _, entry := range entries {
		out = append(out, codeFile{root: root, rootID: rootID, rel: entry.Path,
			abs: filepath.Join(rootPath, filepath.FromSlash(entry.Path))})
	}
	return out
}

// codeIndexIncludeKey shares preparation across identical content projections.
func codeIndexIncludeKey(excludes dependencyDirs, maxBytes int64) string {
	names := make([]string, 0, len(excludes.names))
	for name := range excludes.names {
		names = append(names, name)
	}
	sort.Strings(names)
	prefixes := append([]string(nil), excludes.prefixes...)
	sort.Strings(prefixes)
	return strings.Join([]string{
		"search-content", strconv.FormatInt(maxBytes, 10),
		strings.Join(names, ","), strings.Join(prefixes, ","),
	}, "\x00")
}

func resolveRequestedCodeGeneration(ctx context.Context, catalog *sourcecatalog.Catalog, root CodeRoot, wait time.Duration, includeDependencies bool, selectedPaths ...string) (codeGeneration, error) {
	if !includeDependencies && len(selectedPaths) == 0 {
		return resolveCodeGeneration(ctx, catalog, root, wait)
	}
	reader, err := catalog.Trees.OpenDependencyIndex(ctx, root.ProjectID, sourcecatalog.Root{ID: root.RootID, Path: root.Path}, selectedPaths...)
	if err != nil {
		return codeGeneration{}, err
	}
	return codeGeneration{root: root, rootID: root.RootID, rootPath: root.Path, reader: reader, ephemeral: true}, nil
}

func (e *CodeExecutor) scanGeneration(ctx context.Context, gen codeGeneration, paths pathGlobFilter, spec *codeScanSpec, report *ExecutorReport) error {
	defer func() { _ = gen.reader.Close() }()
	coverage, err := gen.reader.Coverage(ctx)
	if err != nil {
		return err
	}
	report.Code.observeCoverage(coverage)
	if spec.wantFiles && spec.fileCap > 0 {
		err := visitCodePaths(ctx, gen, paths, spec.prefilter, func(files []codeFile) bool {
			report.Code.FilesListed += len(files)
			result := scanCodePaths(ctx, files, *spec)
			report.Hits = append(report.Hits, result.hits...)
			report.Limited = report.Limited || result.partial
			spec.fileCap -= len(result.hits)
			return spec.fileCap > 0
		})
		if err != nil {
			return err
		}
	}
	if !spec.wantLines || spec.lineCap <= 0 {
		return nil
	}
	indexWarming := false
	err = visitCodePages(ctx, gen, paths, spec.lineExcludes, func(entries []sourcecatalog.Entry, files []codeFile) bool {
		if !spec.wantFiles {
			report.Code.FilesListed += len(files)
		}
		jobs, warming := e.contentJobs(ctx, gen, paths.codeScope(), entries, files, *spec, &report.Code)
		indexWarming = indexWarming || warming
		contentSpec := *spec
		contentSpec.wantFiles = false
		result := scanCodeFiles(ctx, jobs, contentSpec)
		report.Hits = append(report.Hits, result.hits...)
		report.Limited = report.Limited || result.partial
		report.SkippedFiles += result.skipped
		report.Code.FilesOpened += result.opened
		report.Code.PrefilterSkipped += result.prefilterSkipped
		spec.lineCap -= len(result.hits)
		return spec.lineCap > 0
	})
	if indexWarming {
		report.Code.IndexWarmingRoots++
	}
	return err
}

// visitCodePages seeks past each excluded directory; its rows can outnumber
// the source that sorts after them.
func visitCodePages(ctx context.Context, gen codeGeneration, paths pathGlobFilter, excludes dependencyDirs, visit func([]sourcecatalog.Entry, []codeFile) bool) error {
	after := ""
	for {
		entries, err := gen.reader.FilePage(ctx, paths.codeScope(), after, sourcecatalog.TreeFilePageLimit)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		after = entries[len(entries)-1].Path
		filtered := entries[:0]
		for _, entry := range entries {
			if dir, excluded := dependencyDirOf(entry.Path, excludes); excluded {
				// No UTF-8 byte is 0xff, so this sorts after every path under dir.
				after = dir + "/\xff"
				break
			}
			if paths.allowsCode(entry.Path) {
				filtered = append(filtered, entry)
			}
		}
		files := codeFilesFromEntries(gen.root, gen.rootID, gen.rootPath, filtered)
		if !visit(filtered, files) {
			return nil
		}
	}
}

func (e *CodeExecutor) contentJobs(ctx context.Context, gen codeGeneration, scope sourcecatalog.FileScope, entries []sourcecatalog.Entry, files []codeFile, spec codeScanSpec, stats *CodeLegReport) ([]codeScanJob, bool) {
	var candidates map[string]bool
	warming := false
	if spec.prefilter.active() && !gen.ephemeral {
		started := time.Now()
		query := sourcecatalog.LiteralQuery{
			FileScope: scope, RootID: gen.rootID, Require: litprefilter.AllOf(spec.prefilter.literalStrings()...),
			IncludeKey: codeIndexIncludeKey(spec.lineExcludes, spec.maxBytes),
			Include: func(entry sourcecatalog.Entry) bool {
				return !underDependencyDir(entry.Path, spec.lineExcludes) && entry.Size <= spec.maxBytes
			},
			Open: func(entry sourcecatalog.Entry) (io.ReadCloser, error) {
				return fseffect.OpenRead(fseffect.Location{Root: gen.rootPath, Rel: filepath.FromSlash(entry.Path)})
			},
		}
		page := e.catalog.Literals.IndexLiteralCandidates(ctx, gen.reader, query, entries)
		warming = page.Warming
		stats.IndexWait += time.Since(started)
		stats.IndexUsed = stats.IndexUsed || page.CachedFiles > 0
		candidates = make(map[string]bool, len(page.Candidates))
		for _, entry := range page.Candidates {
			candidates[entry.Path] = true
		}
	}
	jobs := make([]codeScanJob, 0, len(files))
	for _, file := range files {
		if candidates != nil && !candidates[file.rel] {
			continue
		}
		jobs = append(jobs, codeScanJob{file: file, content: true})
		stats.ContentCandidates++
	}
	return jobs, warming
}
