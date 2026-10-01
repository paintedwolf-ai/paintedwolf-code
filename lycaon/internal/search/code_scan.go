package search

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// codeWorkerCap bounds the file pool on wide hosts.
const codeWorkerCap = 8

func codeWorkerCount() int {
	return max(2, min(runtime.GOMAXPROCS(0)/2, codeWorkerCap))
}

type codeScanSpec struct {
	matcher      codeQueryMatcher
	prefilter    codePrefilter
	wantLines    bool
	wantFiles    bool
	lineCap      int
	fileCap      int
	fileExcludes dependencyDirs
	lineExcludes dependencyDirs
	terms        []string
	maxBytes     int64
}

// codeScanJob is one listed file; content marks it for the line arm.
type codeScanJob struct {
	file    codeFile
	content bool
}

type codeFileOutcome struct {
	seq      int
	fileHit  *Hit
	lineHits []Hit
	// skipped marks an in-scope file whose content could not be searched.
	skipped bool
	// opened and prefiltered feed the leg's telemetry.
	opened      bool
	prefiltered bool
}

// codeScanResult is one root's scan outcome.
type codeScanResult struct {
	hits             []Hit
	partial          bool
	skipped          int
	opened           int
	prefilterSkipped int
}

func scanCodeFiles(ctx context.Context, jobs []codeScanJob, spec codeScanSpec) codeScanResult {
	var result codeScanResult
	if spec.lineCap < 0 {
		spec.lineCap = 0
	}
	if spec.fileCap < 0 {
		spec.fileCap = 0
	}
	if !spec.wantLines {
		spec.lineCap = 0
	}
	if !spec.wantFiles {
		spec.fileCap = 0
	}
	if spec.lineCap <= 0 && spec.fileCap <= 0 {
		result.partial = spec.wantLines || spec.wantFiles
		return result
	}
	if spec.maxBytes <= 0 {
		spec.maxBytes = codeExecutorMaxFileBytes
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	workers := codeWorkerCount()
	queue := make(chan int, workers*2)
	outcomes := make(chan codeFileOutcome, workers*2)
	// Credits bound completed results waiting behind a slow earlier file.
	credits := make(chan struct{}, workers*2)

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				if ctx.Err() != nil {
					outcomes <- codeFileOutcome{seq: i}
					continue
				}
				outcomes <- scanOneCodeFile(ctx, jobs[i], i, spec)
			}
		}()
	}
	go func() {
		defer close(queue)
		for i := range jobs {
			select {
			case credits <- struct{}{}:
			case <-ctx.Done():
				return
			}
			select {
			case queue <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(outcomes)
	}()

	next := 0
	pending := map[int]codeFileOutcome{}
	remainingLines := spec.lineCap
	remainingFiles := spec.fileCap
	drain := func(out codeFileOutcome) {
		if out.skipped {
			result.skipped++
		}
		if out.opened {
			result.opened++
		}
		if out.prefiltered {
			result.prefilterSkipped++
		}
	}
	for out := range outcomes {
		drain(out)
		pending[out.seq] = out
		// Emit in listing order so a cap is deterministic.
		for {
			o, have := pending[next]
			if !have {
				break
			}
			delete(pending, next)
			next++
			<-credits
			var hitCap bool
			result.hits, remainingLines, remainingFiles, hitCap = applyCodeOutcome(result.hits, o, remainingLines, remainingFiles, spec)
			result.partial = result.partial || hitCap
			if remainingLines <= 0 && remainingFiles <= 0 {
				cancel()
				for out := range outcomes {
					drain(out)
				}
				result.partial = true
				return result
			}
		}
	}
	return result
}

func scanOneCodeFile(ctx context.Context, job codeScanJob, seq int, spec codeScanSpec) codeFileOutcome {
	file := job.file
	out := codeFileOutcome{seq: seq}
	if spec.wantFiles && spec.fileCap > 0 && !underDependencyDir(file.rel, spec.fileExcludes) &&
		spec.matcher.matches(codeCandidate{kind: HitKindFile, text: file.rel, path: file.rel}) {
		hit := Hit{
			ID:        stableHitID("file", file.root.ProjectID, file.rootID, file.rel),
			HitKind:   HitKindFile,
			Source:    SourceCode,
			Score:     fileHitScore(file.rel, spec.terms),
			SourceRef: file.rel,
			ProjectID: file.root.ProjectID,
			RootID:    file.rootID,
			Path:      file.rel,
		}
		out.fileHit = &hit
	}
	if !job.content || !spec.wantLines || spec.lineCap <= 0 {
		return out
	}
	out.opened = true
	content, status := readCodeContent(file.abs, spec.maxBytes)
	if status != codeOpenOK {
		out.skipped = status == codeOpenUnsearchable
		return out
	}
	if spec.prefilter.active() && !spec.prefilter.matches(content.bytes) {
		out.prefiltered = true
		return out
	}
	out.lineHits = collectCodeLineHits(ctx, file, content.text(), spec.matcher, spec.terms, spec.lineCap)
	return out
}

func applyCodeOutcome(hits []Hit, out codeFileOutcome, remainingLines, remainingFiles int, spec codeScanSpec) ([]Hit, int, int, bool) {
	partial := false
	if out.fileHit != nil && remainingFiles > 0 {
		hits = append(hits, *out.fileHit)
		remainingFiles--
		if remainingFiles <= 0 {
			partial = spec.wantFiles
		}
	}
	for _, hit := range out.lineHits {
		if remainingLines <= 0 {
			return hits, remainingLines, remainingFiles, true
		}
		hits = append(hits, hit)
		remainingLines--
		if remainingLines <= 0 {
			partial = true
			break
		}
	}
	return hits, remainingLines, remainingFiles, partial
}

func collectCodeLineHits(ctx context.Context, file codeFile, text string, matcher codeQueryMatcher, terms []string, limit int) []Hit {
	var hits []Hit
	lineNo := 0
	start := 0
	// Filter-only queries omit blank lines.
	requireContent := len(terms) == 0
	for start < len(text) {
		if ctx.Err() != nil || len(hits) >= limit {
			break
		}
		lineNo++
		rest := text[start:]
		nl := strings.IndexByte(rest, '\n')
		var line string
		if nl < 0 {
			line = rest
		} else {
			line = rest[:nl]
		}
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		if (!requireContent || strings.TrimSpace(line) != "") &&
			matcher.matches(codeCandidate{kind: HitKindCode, text: line, path: file.rel}) {
			hits = append(hits, Hit{
				ID:        stableHitID("code", file.root.ProjectID, file.rootID, file.rel, strconv.Itoa(lineNo)),
				HitKind:   HitKindCode,
				Source:    SourceCode,
				Score:     codeHitScore(file.rel, line, terms),
				Snippet:   windowCodeSnippet(strings.TrimSpace(line), terms),
				SourceRef: fmt.Sprintf("%s:%d", file.rel, lineNo),
				ProjectID: file.root.ProjectID,
				RootID:    file.rootID,
				Path:      file.rel,
				Line:      lineNo,
			})
		}
		if nl < 0 {
			break
		}
		start += nl + 1
	}
	return hits
}

// codeSnippetMaxBytes bounds one hit's snippet on the wire. A minified or
// generated line can run to tens of kilobytes; the row renders a window.
const codeSnippetMaxBytes = 256

// windowCodeSnippet trims an over-long line to a window around the first
// term match, with ellipses marking the cut ends.
func windowCodeSnippet(line string, terms []string) string {
	if len(line) <= codeSnippetMaxBytes {
		return line
	}
	at := -1
	lower := strings.ToLower(line)
	// ToLower is not length-preserving for every rune; a fold index is only a
	// valid offset into the original line when the lengths agree.
	if len(lower) == len(line) {
		for _, term := range terms {
			if term == ".+" || term == "" {
				continue
			}
			if idx := strings.Index(lower, strings.ToLower(term)); idx >= 0 && (at < 0 || idx < at) {
				at = idx
			}
		}
	}
	start := 0
	if at > codeSnippetMaxBytes/4 {
		start = min(at-codeSnippetMaxBytes/4, len(line)-1)
	}
	end := min(start+codeSnippetMaxBytes, len(line))
	// Snippet boundaries preserve complete UTF-8 sequences.
	for start > 0 && !utf8.RuneStart(line[start]) {
		start--
	}
	for end < len(line) && !utf8.RuneStart(line[end]) {
		end++
	}
	out := line[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(line) {
		out += "…"
	}
	return out
}
