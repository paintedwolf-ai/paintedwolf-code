package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOrderedScansBoundLookahead(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(fmt.Sprintf("preview=%v", preview), func(t *testing.T) {
			window := codeWorkerCount() * 2
			gate := &scanOrderGate{calls: make(chan struct{}, window*4), release: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(gate.release) })
			files := orderedScanFixture(t, window*4)
			done := make(chan int, 1)
			go func() {
				if preview {
					out := previewReplaceFiles(t.Context(), files, gatedReplaceMatcher{gate: gate}, codePrefilter{}, "new", 1, 1)
					done <- len(out.files)
					return
				}
				jobs := make([]codeScanJob, len(files))
				for i, file := range files {
					jobs[i] = codeScanJob{file: file, content: true}
				}
				out := scanCodeFiles(t.Context(), jobs, codeScanSpec{matcher: gatedCodeMatcher{gate: gate}, wantLines: true, lineCap: 1})
				done <- len(out.hits)
			}()
			for range window {
				select {
				case <-gate.calls:
				case <-time.After(30 * time.Second):
					t.Fatal("scan did not fill its lookahead window")
				}
			}
			select {
			case <-gate.calls:
				t.Error("scan advanced beyond the bounded lookahead window")
			case <-time.After(100 * time.Millisecond):
			}
			release.Do(func() { close(gate.release) })
			select {
			case count := <-done:
				if count != 1 {
					t.Fatalf("retained %d results, want 1", count)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("scan did not join workers after reaching the result cap")
			}
		})
	}
}

func orderedScanFixture(t *testing.T, count int) []codeFile {
	t.Helper()
	root := t.TempDir()
	files := make([]codeFile, count)
	for i := range files {
		rel := fmt.Sprintf("%03d.txt", i)
		body := "hit"
		if i == 0 {
			body = "first"
		}
		abs := filepath.Join(root, rel)
		testutil.FailErr(t, "write ordered scan fixture", os.WriteFile(abs, []byte(body), 0o600))
		files[i] = codeFile{abs: abs, rel: rel}
	}
	return files
}

type scanOrderGate struct {
	calls   chan struct{}
	release chan struct{}
}

func (g *scanOrderGate) visit(text string) {
	g.calls <- struct{}{}
	if text == "first" {
		<-g.release
	}
}

type gatedCodeMatcher struct{ gate *scanOrderGate }

func (m gatedCodeMatcher) matches(candidate codeCandidate) bool {
	m.gate.visit(candidate.text)
	return true
}

type gatedReplaceMatcher struct{ gate *scanOrderGate }

func (gatedReplaceMatcher) matches(string) bool { return true }

func (m gatedReplaceMatcher) findAll(content, replacement string) []replacementMatch {
	m.gate.visit(content)
	return []replacementMatch{{start: 0, end: len(content), fragment: replacement}}
}

func TestCodeLineHitsRespectBudgetAndCancellation(t *testing.T) {
	matcher, err := compileCodeQuery(TextExpr{Text: "hit"}, MatchFlags{})
	testutil.FailErr(t, "compile dense scan query", err)
	text := strings.Repeat("hit\n", 100000)
	hits := collectCodeLineHits(t.Context(), codeFile{rel: "generated.txt"}, text, matcher, []string{"hit"}, 3)
	if len(hits) != 3 || hits[2].Line != 3 {
		t.Fatalf("dense scan retained %d hits, want first 3", len(hits))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if hits := collectCodeLineHits(ctx, codeFile{rel: "generated.txt"}, text, matcher, []string{"hit"}, 3); len(hits) != 0 {
		t.Fatalf("canceled scan retained %d hits", len(hits))
	}
}
