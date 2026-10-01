package fileoutline

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestAnalysisCacheBacksOffFailedDefinitions(t *testing.T) {
	for _, skip := range []repomap.SkipStats{{ParseIncomplete: 1}, {ParseFailed: 1}} {
		cache := newAnalysisCache(8, 1<<20)
		first := cache.analyze(t.Context(), "main.go", []byte("same"), func() Result {
			return Result{Diagnostics: &repomap.Diagnostics{SkipReasons: skip}}
		})
		if first.DefinitionError() == nil || cache.stats().Entries != 1 {
			t.Fatalf("failed definition analysis lost its retry delay: %+v", cache.stats())
		}
		if skip.ParseIncomplete != 0 && !errors.Is(first.DefinitionError(), repomap.ErrDefinitionIncomplete) {
			t.Fatalf("incomplete error = %v", first.DefinitionError())
		}
		blocked := cache.analyze(t.Context(), "main.go", []byte("same"), func() Result { t.Fatal("retried before the delay elapsed"); return Result{} })
		if blocked.DefinitionError() == nil {
			t.Fatal("cached failure became an empty outline")
		}
		entry := cache.entries[analysisCacheKey("main.go", []byte("same"))].Value.(*analysisCacheEntry)
		entry.expires = time.Now().Add(-time.Second)
		second := cache.analyze(t.Context(), "main.go", []byte("same"), func() Result {
			return Result{Symbols: []Symbol{{Kind: "function", Name: "Run", Line: 1}}}
		})
		if second.DefinitionError() != nil || len(second.Symbols) != 1 || cache.stats().Entries != 1 {
			t.Fatalf("subsequent successful analysis was not retained: %+v, %+v", second, cache.stats())
		}
	}
}

func TestAnalysisCacheSharesContentAcrossDirectories(t *testing.T) {
	cache := newAnalysisCache(8, 1<<20)
	var builds int
	build := func() Result {
		builds++
		return Result{Language: "go", Symbols: []Symbol{{Kind: "function", Name: "Run", Line: 1}}}
	}

	first := cache.analyze(t.Context(), "one/main.go", []byte("func Run() {}"), build)
	second := cache.analyze(t.Context(), "two/main.go", []byte("func Run() {}"), build)
	if builds != 1 {
		t.Fatalf("builds = %d, want 1", builds)
	}
	if len(first.Symbols) != 1 || len(second.Symbols) != 1 {
		t.Fatalf("shared result missing: first=%+v second=%+v", first, second)
	}
	stats := cache.stats()
	if stats.Hits != 1 || stats.Misses != 1 || stats.Entries != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAnalysisCacheSeparatesSemanticFilename(t *testing.T) {
	cache := newAnalysisCache(8, 1<<20)
	var builds int
	build := func() Result {
		builds++
		return Result{}
	}
	src := []byte("value: true\n")
	cache.analyze(t.Context(), "config.yaml", src, build)
	cache.analyze(t.Context(), "config.json", src, build)
	if builds != 2 {
		t.Fatalf("builds = %d, want 2", builds)
	}
}

func TestAnalysisCacheReturnsDeepCopies(t *testing.T) {
	cache := newAnalysisCache(8, 1<<20)
	parses := true
	original := Result{
		Symbols:     []Symbol{{Kind: "function", Name: "Run", Line: 1}},
		Definitions: []repomap.DefinitionSpan{{Kind: "function", Name: "Run", StartRow: 0}},
		Errors:      []SyntaxDiagnostic{{Row: 1, Kind: syntaxhealth.DiagnosticError}},
		Parses:      &parses,
		Diagnostics: &repomap.Diagnostics{Hint: "kept"},
		LogDigest: &logoutline.Digest{
			Fields: []logoutline.FieldStat{{Key: "level"}},
			Facets: []logoutline.Facet{{Key: "level", Values: []logoutline.FacetValue{{Value: "info"}}}},
		},
	}
	first := cache.analyze(t.Context(), "main.go", []byte("same"), func() Result { return original })
	first.Symbols[0].Name = "Changed"
	first.Definitions[0].Name = "Changed"
	first.Errors[0].Kind = "changed"
	*first.Parses = false
	first.Diagnostics.Hint = "changed"
	first.LogDigest.Fields[0].Key = "changed"
	first.LogDigest.Facets[0].Values[0].Value = "changed"

	second := cache.analyze(t.Context(), "main.go", []byte("same"), func() Result {
		t.Fatal("cache miss")
		return Result{}
	})
	if second.Symbols[0].Name != "Run" || second.Definitions[0].Name != "Run" ||
		second.Errors[0].Kind != syntaxhealth.DiagnosticError || !*second.Parses ||
		second.Diagnostics.Hint != "kept" || second.LogDigest.Fields[0].Key != "level" ||
		second.LogDigest.Facets[0].Values[0].Value != "info" {
		t.Fatalf("cached result was mutated: %+v", second)
	}
}

func TestAnalysisCacheJoinsConcurrentBuild(t *testing.T) {
	cache := newAnalysisCache(8, 1<<20)
	var builds atomic.Int32
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	build := func() Result {
		if builds.Add(1) == 1 {
			close(buildStarted)
		}
		<-releaseBuild
		return Result{Language: "go"}
	}

	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			cache.analyze(t.Context(), "main.go", []byte("func main() {}"), build)
		}()
	}
	close(start)
	<-buildStarted
	deadline := time.Now().Add(time.Second)
	for cache.stats().Joins != callers-1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(releaseBuild)
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatalf("builds = %d, want 1", builds.Load())
	}
	if got := cache.stats().Joins; got != callers-1 {
		t.Fatalf("joins = %d, want %d", got, callers-1)
	}
}

func TestAnalysisCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newAnalysisCache(2, 1<<20)
	build := func(name string) func() Result {
		return func() Result { return Result{Language: name} }
	}
	cache.analyze(t.Context(), "a.go", []byte("a"), build("a"))
	cache.analyze(t.Context(), "b.go", []byte("b"), build("b"))
	cache.analyze(t.Context(), "a.go", []byte("a"), build("unexpected"))
	cache.analyze(t.Context(), "c.go", []byte("c"), build("c"))
	cache.analyze(t.Context(), "b.go", []byte("b"), build("rebuilt"))
	stats := cache.stats()
	if stats.Entries != 2 || stats.Evictions != 2 || stats.Misses != 4 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAnalysisCacheEnforcesByteBudget(t *testing.T) {
	result := Result{Language: "go", Symbols: []Symbol{{Kind: "function", Name: "Run", Line: 1}}}
	weight := analysisResultBytes(analysisCacheKey("a.go", []byte("a")), result)
	cache := newAnalysisCache(20, weight*4)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		cache.analyze(t.Context(), name+".go", []byte(name), func() Result { return result })
	}
	stats := cache.stats()
	if stats.Bytes > weight*4 || stats.Entries != 4 || stats.Evictions != 1 {
		t.Fatalf("stats = %+v, entry weight = %d", stats, weight)
	}
}

func TestAnalysisCacheBypassesResultThatWouldMonopolizeBudget(t *testing.T) {
	cache := newAnalysisCache(8, 400)
	cache.analyze(t.Context(), "main.go", []byte("same"), func() Result {
		return Result{Symbols: []Symbol{{Kind: "function", Name: strings.Repeat("x", 200)}}}
	})
	stats := cache.stats()
	if stats.Entries != 0 || stats.Bypasses != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAnalysisCacheInvalidatesFailureWhenTimeoutChanges(t *testing.T) {
	fsys := fstest.MapFS{config.SourceParsing.String(): &fstest.MapFile{Data: []byte("version: 1\nvalidation_timeout_ms: 30000\nanalysis_timeout_ms: 1\n")}}
	configtest.Use(t, fsys)
	cache := newAnalysisCache(8, 1<<20)
	calls := 0
	build := func() Result {
		calls++
		return Result{ParseFailure: &tsparse.Failure{Reason: "timeout", Language: "swift", TimeoutMS: 1, SourceBytes: 100}}
	}
	first := cache.analyze(t.Context(), "a.swift", []byte("same"), build)
	first.ParseFailure.Reason = "modified"
	again := cache.analyze(t.Context(), "a.swift", []byte("same"), build)
	if calls != 1 || again.ParseFailure.Reason != "timeout" {
		t.Fatalf("cached failure was not isolated: calls=%d result=%+v", calls, again)
	}
	fsys[config.SourceParsing.String()].Data = []byte("version: 1\nvalidation_timeout_ms: 30000\nanalysis_timeout_ms: 10000\n")
	cache.analyze(t.Context(), "a.swift", []byte("same"), build)
	if calls != 2 {
		t.Fatalf("configuration correction did not invalidate failure: builds=%d", calls)
	}
}

func TestAnalysisCancellationDoesNotPoisonSharedSource(t *testing.T) {
	cache := newAnalysisCache(8, 1<<20)
	ctx, cancel := context.WithCancel(t.Context())
	started, release := make(chan struct{}), make(chan struct{})
	firstDone := make(chan Result, 1)
	go func() {
		firstDone <- cache.analyze(ctx, "a.go", []byte("same"), func() Result {
			close(started)
			<-release
			return Result{Language: "go"}
		})
	}()
	<-started
	secondDone := make(chan Result, 1)
	go func() {
		secondDone <- cache.analyze(t.Context(), "a.go", []byte("same"), func() Result {
			return Result{Language: "go", Source: "rebuilt"}
		})
	}()
	testutil.WaitFor(t, time.Second, func() bool { return cache.stats().Joins == 1 })
	cancel()
	close(release)
	first, second := <-firstDone, <-secondDone
	if !errors.Is(first.DefinitionError(), context.Canceled) || second.Source != "rebuilt" || second.DefinitionError() != nil {
		t.Fatalf("shared cancellation: first=%+v second=%+v", first, second)
	}
}

func TestAnalysisWaiterCanCancelIndependently(t *testing.T) {
	cache := newAnalysisCache(8, 1<<20)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		cache.analyze(t.Context(), "a.go", []byte("same"), func() Result {
			close(started)
			<-release
			return Result{Language: "go"}
		})
	}()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiter := make(chan Result, 1)
	go func() {
		waiter <- cache.analyze(ctx, "a.go", []byte("same"), func() Result { t.Error("joined caller started parsing"); return Result{} })
	}()
	testutil.WaitFor(t, time.Second, func() bool { return cache.stats().Joins == 1 })
	cancel()
	result := <-waiter
	close(release)
	<-finished
	if !errors.Is(result.DefinitionError(), context.Canceled) {
		t.Fatalf("canceled waiter result: %+v", result)
	}
}
