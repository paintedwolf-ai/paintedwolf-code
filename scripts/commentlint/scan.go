package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func lint(opts options, targets []string) (report, error) {
	started := time.Now()
	jobs, skipped, err := collectFiles(opts, targets)
	rep := newReport()
	rep.FilesSkipped = skipped
	rep.FilesVisited = len(jobs) + skipped
	if err != nil {
		return rep, err
	}
	results := make([]fileResult, len(jobs))
	queue := make(chan int)
	languages := languageRegistry{values: map[string]*gotreesitter.Language{}}
	var workers sync.WaitGroup
	for range opts.workers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			engines := map[string]syntaxParser{}
			for index := range queue {
				results[index] = scanFile(opts, jobs[index], engines, &languages)
				if results[index].timedOut {
					delete(engines, results[index].language)
				}
			}
		}()
	}
	for index := range jobs {
		queue <- index
	}
	close(queue)
	workers.Wait()
	for index, result := range results {
		if result.err != nil {
			return rep, result.err
		}
		mergeResult(&rep, jobs[index], result)
	}
	rep.ElapsedMilliseconds = time.Since(started).Milliseconds()
	return rep, nil
}

func newReport() report {
	return report{
		Findings: []finding{}, ParseTimeouts: []string{},
		Languages: map[string]languageReport{}, UnsupportedExtensions: map[string]int{},
	}
}

func collectFiles(opts options, targets []string) ([]fileJob, int, error) {
	seen := map[string]struct{}{}
	var jobs []fileJob
	skipped := 0
	for _, target := range targets {
		path := target
		if !filepath.IsAbs(path) {
			path = filepath.Join(opts.root, path)
		}
		err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(opts.root, path)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if rel != "." && (excludedDir(entry.Name()) || excludedByFlag(rel, opts.excludes) || isRepoRoot(path)) {
					return filepath.SkipDir
				}
				return nil
			}
			if _, ok := seen[path]; ok {
				return nil
			}
			seen[path] = struct{}{}
			if excludedFile(entry) || excludedByFlag(rel, opts.excludes) {
				skipped++
				return nil
			}
			jobs = append(jobs, fileJob{path: path, rel: filepath.ToSlash(rel)})
			return nil
		})
		if err != nil {
			return nil, skipped, err
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].rel < jobs[j].rel })
	return jobs, skipped, nil
}

func scanFile(opts options, job fileJob, engines map[string]syntaxParser, languages *languageRegistry) fileResult {
	grammar := grammars.DetectLanguage(job.path)
	if grammar == nil || grammar.Language == nil {
		extension := strings.ToLower(filepath.Ext(job.path))
		if extension == "" {
			extension = "<none>"
		}
		return fileResult{unsupportedExtension: extension}
	}
	info, err := os.Stat(job.path)
	if err != nil {
		return fileResult{err: err}
	}
	if !info.Mode().IsRegular() || info.Size() > maxSourceBytes {
		return fileResult{language: grammar.Name, skipped: true}
	}
	src, err := os.ReadFile(job.path) // #nosec G304 -- explicit lint target
	if err != nil {
		return fileResult{language: grammar.Name, err: err}
	}
	if generatedSource(src) {
		return fileResult{language: grammar.Name, skipped: true}
	}
	comments, lexed := lexicalComments(grammar.Name, src)
	result := fileResult{language: grammar.Name, scanned: true, comments: len(comments)}
	if lexed {
		for _, item := range comments {
			result.findings = append(result.findings, inspect(job.rel, item, opts)...)
		}
		// Diff comments are fully defined by a column-one prefix.
		if len(result.findings) == 0 || grammar.Name == "diff" {
			return result
		}
		result.findings = nil
	}
	engine, ok := engines[grammar.Name]
	if !ok {
		engine.language = languages.get(grammar.Name, grammar.Language)
		engine.parser = gotreesitter.NewParser(engine.language)
		engines[grammar.Name] = engine
	}
	comments, timedOut, err := extractComments(engine, grammar.Name, src, opts.parseTimeout)
	if err != nil {
		return fileResult{language: grammar.Name, err: fmt.Errorf("parse %s: %w", job.rel, err)}
	}
	result.validated = !timedOut
	result.timedOut = timedOut
	result.comments = len(comments)
	for _, item := range comments {
		result.findings = append(result.findings, inspect(job.rel, item, opts)...)
	}
	return result
}

type languageRegistry struct {
	mu     sync.Mutex
	values map[string]*gotreesitter.Language
}

func (registry *languageRegistry) get(name string, load func() *gotreesitter.Language) *gotreesitter.Language {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if language := registry.values[name]; language != nil {
		return language
	}
	language := load()
	registry.values[name] = language
	return language
}

func mergeResult(rep *report, job fileJob, result fileResult) {
	if result.unsupportedExtension != "" {
		rep.FilesUnsupported++
		rep.UnsupportedExtensions[result.unsupportedExtension]++
		return
	}
	if result.skipped {
		rep.FilesSkipped++
		return
	}
	stats := rep.Languages[result.language]
	stats.Files++
	if result.scanned {
		rep.FilesScanned++
		rep.Comments += result.comments
		stats.Scanned++
		stats.Comments += result.comments
	}
	if result.timedOut {
		rep.ParseTimeouts = append(rep.ParseTimeouts, job.rel)
		stats.Timeouts++
	}
	if result.validated {
		rep.FilesValidated++
		stats.Validated++
	}
	rep.Findings = append(rep.Findings, result.findings...)
	rep.Languages[result.language] = stats
}

func excludedDir(name string) bool {
	switch name {
	case ".git", ".bin", ".task", "build", "dist", "licenses", "node_modules", "target", "vendor":
		return true
	default:
		return false
	}
}

func excludedFile(entry fs.DirEntry) bool {
	name := entry.Name()
	if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
		return true
	}
	if name == "LICENSE" || strings.HasPrefix(name, "LICENSE.") {
		return true
	}
	return strings.Contains(name, ".generated.")
}

func excludedByFlag(rel string, excludes []string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	for _, value := range excludes {
		value = filepath.ToSlash(filepath.Clean(value))
		if rel == value || strings.HasPrefix(rel, strings.TrimSuffix(value, "/")+"/") {
			return true
		}
	}
	return false
}

func generatedSource(src []byte) bool {
	if len(src) > 2048 {
		src = src[:2048]
	}
	text := string(src)
	return strings.Contains(text, "Code generated") && strings.Contains(text, "DO NOT EDIT")
}
