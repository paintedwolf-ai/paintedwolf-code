package gotreesitter_test

// Diagnostic reproduction for the issue #380 report: a ~50x nondeterministic
// wall-clock swing on a single-char INSERT incremental reparse of a ~137KB Go
// file (reported ~4.6-5.0s, observed once at ~97ms), with a same-file DELETE
// running ~24x faster despite allocating MORE nodes, and both far slower than
// a fresh full parse of the same file.
//
// All tests here are gated behind explicit environment variables so they
// never run as part of `go test ./...`. Run individually, e.g.:
//
//	GTS_REPRO_INSERT=1 GTS_REPRO_FILE=/path/to/137kb.go GTS_REPRO_ITERS=25 \
//	  go test -run TestReproInsertRetryInstability -v .
//
// Findings (2026-07, this box, linux/amd64):
//   - Using this repo's own tree.go (137030 bytes, a real Go file, not
//     synthetic) with a single-char insert near the top (offset ~200, inside
//     a struct field's type name -- syntactically valid, mirrors an in-place
//     keystroke), 25 iterations with a FRESH Parser + fresh Tree per
//     iteration produced a CONSISTENT ~1.2-1.9s per incremental parse -- no
//     bimodality was observed under pure fresh-parser conditions.
//   - AcceptedErrorRetryAttempts was 0 in every iteration: the accepted-error
//     retry rung (retryIncrementalAcceptedErrorWithDFA /
//     retryIncrementalAcceptedErrorWithBaseMergeCap in parser_retry.go), the
//     reporter's prime suspect, never fired. It is not implicated in this
//     reproduction.
//   - A full fresh Parse() of the same file takes ~90-160ms -- i.e. the
//     incremental insert is consistently ~10-15x a full parse, even before
//     accounting for the further nondeterminism explained in
//     issue380_incremental_insert_cache_warmup_nondeterminism_repro_test.go.
//   - reuseRejectRootNonLeafChanged is high (87) and reusedSubtrees is tiny
//     (60) relative to the file. Campaign O(edit) workstream W1
//     (spec.campaign.oedit) replaced the CSS/cmake-only
//     languageAllowsForestTopLevelSiblingReuse admission gate with a
//     fragility-gated one (reuseCursor.topLevelSiblingBlockSpliceEligible,
//     incremental.go) available to every language, but on this exact
//     repro the practical blocker turned out to be a separate, deeper
//     gap: tryReuseSubtree runs before the pending reduce chain that
//     would settle s.top().state to the same state the old tree recorded
//     as the candidate's PreGotoState() -- see the W1 PR description for
//     the full root-cause trace. Non-leaf/structural subtree reuse across
//     an edit remains effectively unavailable for Go's top-level
//     declaration list until that settling gap is closed (tracked as a
//     W1 follow-up, not attempted here per decision-0008's rider against
//     unproven reuse-bar lifts). An edit anywhere near the top of a large Go file
//     therefore forces near-total GLR re-derivation of everything after it
//     (tokensConsumed ~= the file's full token count), which is inherently
//     far more expensive than the cheap subtree-swap incremental parsing is
//     supposed to provide.
//   - A CPU profile of one slow (~1.2s) run shows the time dominated by
//     (*Parser).cNodeErrorCost (42.5% flat), cSymbolVisibleLang (20.5%
//     flat), and (*Parser).cNodeVisibleSubtreeCount (10.2% flat, 35.4%
//     cumulative) -- the C-parity error-cost/visible-subtree-count cost
//     model used to compare competing live GLR stacks
//     (parser_recover_c.go's cStackErrorCost / cVersionStatus /
//     cBetterVersionExists, invoked from ordinary multi-stack GLR
//     dispatch/merge, not only from genuine syntax-error recovery -- see the
//     doc comment on cNodeMemoCacheInitialSize, which explicitly says this
//     path is "warm on every capable parse, clean or not"). See
//     issue380_incremental_insert_cache_warmup_nondeterminism_repro_test.go
//     for why this cost model is sometimes O(1)-amortized (fast) and
//     sometimes O(depth) uncached per call (catastrophically slow) for
//     otherwise-identical input.

import (
	"bytes"
	"fmt"
	"os"
	"runtime/pprof"
	"testing"
	"time"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func issue380PointForOffset(src []byte, off int) gotreesitter.Point {
	prefix := src[:off]
	row := bytes.Count(prefix, []byte{'\n'})
	col := off
	if idx := bytes.LastIndexByte(prefix, '\n'); idx >= 0 {
		col = off - idx - 1
	}
	return gotreesitter.Point{Row: uint32(row), Column: uint32(col)}
}

func issue380EnvInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

// issue380ValidOffset bounds-checks an env-supplied GTS_REPRO_OFFSET against
// src before any test does src[:offset] / src[offset:] slicing, so a
// misconfigured or stale offset (e.g. GTS_REPRO_FILE swapped for a smaller
// fixture without updating GTS_REPRO_OFFSET) fails the test cleanly via
// t.Fatalf instead of panicking with a raw "slice bounds out of range".
// need is how many bytes must remain at/after offset: 0 for an insert point,
// 1 for a single-byte delete.
func issue380ValidOffset(t *testing.T, src []byte, offset, need int) {
	t.Helper()
	if offset < 0 || offset+need > len(src) {
		t.Fatalf("GTS_REPRO_OFFSET=%d out of range for %d-byte source (want offset in [0, %d])", offset, len(src), len(src)-need)
	}
}

// TestReproInsertRetryInstability runs ParseIncrementalProfiled in a loop
// with a fresh Parser + fresh Tree per iteration for a single-char INSERT
// near the top of a real ~137KB Go file, printing wall time and the
// AcceptedErrorRetry*/reuse-reject profile fields the issue points at.
func TestReproInsertRetryInstability(t *testing.T) {
	if os.Getenv("GTS_REPRO_INSERT") == "" {
		t.Skip("set GTS_REPRO_INSERT=1 to run")
	}
	src, err := os.ReadFile(os.Getenv("GTS_REPRO_FILE"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lang := grammars.GoLanguage()
	iterations := issue380EnvInt("GTS_REPRO_ITERS", 20)
	insertOffset := issue380EnvInt("GTS_REPRO_OFFSET", 200)
	issue380ValidOffset(t, src, insertOffset, 0)

	fmt.Printf("file=%d bytes insertOffset=%d iterations=%d\n", len(src), insertOffset, iterations)

	for i := 0; i < iterations; i++ {
		parser := gotreesitter.NewParser(lang)
		oldTree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("[%d] base parse error: %v", i, err)
		}

		newSrc := make([]byte, 0, len(src)+1)
		newSrc = append(newSrc, src[:insertOffset]...)
		newSrc = append(newSrc, 'x')
		newSrc = append(newSrc, src[insertOffset:]...)

		startPoint := issue380PointForOffset(src, insertOffset)
		newEndPoint := gotreesitter.Point{Row: startPoint.Row, Column: startPoint.Column + 1}
		oldTree.Edit(gotreesitter.InputEdit{
			StartByte:   uint32(insertOffset),
			OldEndByte:  uint32(insertOffset),
			NewEndByte:  uint32(insertOffset + 1),
			StartPoint:  startPoint,
			OldEndPoint: startPoint,
			NewEndPoint: newEndPoint,
		})

		start := time.Now()
		newTree, profile, err := parser.ParseIncrementalProfiled(newSrc, oldTree)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("[%d] incremental parse error: %v", i, err)
		}
		hasError := newTree != nil && newTree.RootNode() != nil && newTree.RootNode().HasError()
		fmt.Printf("iter=%d elapsed=%s hasError=%v acceptedErrAttempts=%d acceptedErrAdopted=%v acceptedErrCause=%v reparseNanos=%d reuseCursorNanos=%d tokensConsumed=%d maxStacksSeen=%d reuseRejectRootNonLeafChanged=%d reusedSubtrees=%d newNodes=%d oldTreeReuseRoute=%v stopReason=%v\n",
			i, elapsed, hasError,
			profile.AcceptedErrorRetryAttempts, profile.AcceptedErrorRetryAdopted, profile.AcceptedErrorRetryCause,
			profile.ReparseNanos, profile.ReuseCursorNanos, profile.TokensConsumed, profile.MaxStacksSeen,
			profile.ReuseRejectRootNonLeafChanged, profile.ReusedSubtrees, profile.NewNodesAllocated,
			profile.OldTreeReuseRoute, profile.StopReason,
		)

		newTree.Release()
		oldTree.Release()
	}
}

// TestReproDeleteAsymmetry is the DELETE counterpart, fresh Parser/Tree per
// iteration, for direct comparison against the INSERT case above.
func TestReproDeleteAsymmetry(t *testing.T) {
	if os.Getenv("GTS_REPRO_DELETE") == "" {
		t.Skip("set GTS_REPRO_DELETE=1 to run")
	}
	src, err := os.ReadFile(os.Getenv("GTS_REPRO_FILE"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lang := grammars.GoLanguage()
	iterations := issue380EnvInt("GTS_REPRO_ITERS", 20)
	deleteOffset := issue380EnvInt("GTS_REPRO_OFFSET", 200)
	issue380ValidOffset(t, src, deleteOffset, 1)

	for i := 0; i < iterations; i++ {
		parser := gotreesitter.NewParser(lang)
		oldTree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("[%d] base parse error: %v", i, err)
		}

		newSrc := make([]byte, 0, len(src)-1)
		newSrc = append(newSrc, src[:deleteOffset]...)
		newSrc = append(newSrc, src[deleteOffset+1:]...)

		startPoint := issue380PointForOffset(src, deleteOffset)
		oldEndPoint := issue380PointForOffset(src, deleteOffset+1)
		oldTree.Edit(gotreesitter.InputEdit{
			StartByte:   uint32(deleteOffset),
			OldEndByte:  uint32(deleteOffset + 1),
			NewEndByte:  uint32(deleteOffset),
			StartPoint:  startPoint,
			OldEndPoint: oldEndPoint,
			NewEndPoint: startPoint,
		})

		start := time.Now()
		newTree, profile, err := parser.ParseIncrementalProfiled(newSrc, oldTree)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("[%d] incremental parse error: %v", i, err)
		}
		hasError := newTree != nil && newTree.RootNode() != nil && newTree.RootNode().HasError()
		fmt.Printf("DEL iter=%d elapsed=%s hasError=%v acceptedErrAttempts=%d acceptedErrAdopted=%v acceptedErrCause=%v reparseNanos=%d reuseCursorNanos=%d tokensConsumed=%d maxStacksSeen=%d reuseRejectRootNonLeafChanged=%d reusedSubtrees=%d newNodes=%d oldTreeReuseRoute=%v stopReason=%v\n",
			i, elapsed, hasError,
			profile.AcceptedErrorRetryAttempts, profile.AcceptedErrorRetryAdopted, profile.AcceptedErrorRetryCause,
			profile.ReparseNanos, profile.ReuseCursorNanos, profile.TokensConsumed, profile.MaxStacksSeen,
			profile.ReuseRejectRootNonLeafChanged, profile.ReusedSubtrees, profile.NewNodesAllocated,
			profile.OldTreeReuseRoute, profile.StopReason,
		)

		newTree.Release()
		oldTree.Release()
	}
}

// TestReproFullParse benchmarks a plain full parse of the same file for a
// baseline (the issue reports 78ms; the incremental insert above should be
// far slower than this).
func TestReproFullParse(t *testing.T) {
	if os.Getenv("GTS_REPRO_FULL") == "" {
		t.Skip("set GTS_REPRO_FULL=1 to run")
	}
	src, err := os.ReadFile(os.Getenv("GTS_REPRO_FILE"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lang := grammars.GoLanguage()
	iterations := issue380EnvInt("GTS_REPRO_ITERS", 10)
	for i := 0; i < iterations; i++ {
		parser := gotreesitter.NewParser(lang)
		start := time.Now()
		tree, err := parser.Parse(src)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("[%d] full parse error: %v", i, err)
		}
		fmt.Printf("FULL iter=%d elapsed=%s\n", i, elapsed)
		tree.Release()
	}
}

// TestReproCPUProfile captures a CPU profile of a single slow incremental
// insert, for `go tool pprof -top`. Set GTS_REPRO_PROFILE_OUT to the output
// path.
func TestReproCPUProfile(t *testing.T) {
	if os.Getenv("GTS_REPRO_PROFILE") == "" {
		t.Skip("set GTS_REPRO_PROFILE=1 to run")
	}
	src, err := os.ReadFile(os.Getenv("GTS_REPRO_FILE"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lang := grammars.GoLanguage()
	insertOffset := issue380EnvInt("GTS_REPRO_OFFSET", 200)
	issue380ValidOffset(t, src, insertOffset, 0)

	newSrc := make([]byte, 0, len(src)+1)
	newSrc = append(newSrc, src[:insertOffset]...)
	newSrc = append(newSrc, 'x')
	newSrc = append(newSrc, src[insertOffset:]...)

	parser := gotreesitter.NewParser(lang)
	oldTree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("base parse error: %v", err)
	}
	startPoint := issue380PointForOffset(src, insertOffset)
	newEndPoint := gotreesitter.Point{Row: startPoint.Row, Column: startPoint.Column + 1}
	oldTree.Edit(gotreesitter.InputEdit{
		StartByte:   uint32(insertOffset),
		OldEndByte:  uint32(insertOffset),
		NewEndByte:  uint32(insertOffset + 1),
		StartPoint:  startPoint,
		OldEndPoint: startPoint,
		NewEndPoint: newEndPoint,
	})

	out := os.Getenv("GTS_REPRO_PROFILE_OUT")
	if out == "" {
		out = "cpu.prof"
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		t.Fatalf("start cpu profile: %v", err)
	}
	start := time.Now()
	newTree, profile, err := parser.ParseIncrementalProfiled(newSrc, oldTree)
	elapsed := time.Since(start)
	pprof.StopCPUProfile()
	if err != nil {
		t.Fatalf("incremental parse error: %v", err)
	}
	fmt.Printf("elapsed=%s acceptedErrAttempts=%d reparseNanos=%d maxStacksSeen=%d tokensConsumed=%d reuseRejectRootNonLeafChanged=%d\n",
		elapsed, profile.AcceptedErrorRetryAttempts, profile.ReparseNanos, profile.MaxStacksSeen,
		profile.TokensConsumed, profile.ReuseRejectRootNonLeafChanged)
	newTree.Release()
	oldTree.Release()
}
