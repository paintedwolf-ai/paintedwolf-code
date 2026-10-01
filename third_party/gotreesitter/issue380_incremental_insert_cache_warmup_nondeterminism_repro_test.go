package gotreesitter_test

// This file isolates the ROOT CAUSE of the issue #380 nondeterminism: it is
// NOT the accepted-error retry ladder (parser_retry.go), and it is NOT the
// already-acknowledged PrecDynamic-tie / GLR-merge-selection nondeterminism
// documented in parser_retry.go's "go" case comments (that class of
// nondeterminism picks a DIFFERENT tree; ours picks the SAME tree, same
// tokensConsumed, same node counts, every time -- only the wall-clock time
// differs).
//
// Mechanism (parser_recover_c.go):
//   - Every C-recovery-capable language parse (Go included, via
//     errorCostCompetitionEnabled/CRecoveryCostCompetitionEnabledByDefault)
//     uses a pointer-keyed 2-way set-associative memo cache, p.cNodeMemoCache,
//     to avoid re-walking whole subtrees when comparing competing live GLR
//     stacks (cNodeErrorCost / cNodeVisibleSubtreeCount, called from
//     cStackErrorCost / cVersionStatus / cBetterVersionExists -- ordinary
//     multi-stack GLR dispatch, not only genuine syntax-error recovery; see
//     the doc comment on cNodeMemoCacheInitialSize in parser_recover_c.go,
//     which explicitly says this path is "warm on every capable parse,
//     clean or not").
//   - That cache starts SMALL (cNodeMemoCacheInitialSize = 128 entries, 64
//     two-way sets) at the start of every parse where len(p.cNodeMemoCache)
//     == 0 (parser.go, configureParseCaps's caller around line 4008).
//   - It only grows to its full working-set size (cNodeMemoCacheSize = 16384
//     entries) via growCNodeMemoCache(), which is called from EXACTLY ONE
//     call site: parser_recover_c.go's cHandleError, the FIRST TIME a
//     lineage on THIS Parser instance actually enters genuine C-style error
//     recovery (p.crecoveryEnteredErrorState).
//   - Crucially, growCNodeMemoCache() NEVER shrinks the cache back down, and
//     nothing else resets p.cNodeMemoCache to empty: it is a field on the
//     *Parser* struct, not per-parse state. So once ANY parse on a given
//     Parser instance -- including a totally UNRELATED parse of a different,
//     genuinely-malformed file -- triggers real error recovery even once,
//     that Parser instance is permanently "warm" for the rest of its life:
//     every subsequent parse on it gets the full 16384-entry cache from the
//     start, even though the growth trigger (genuine syntax error) has
//     nothing to do with the (clean, syntactically valid) file/edit being
//     reparsed now.
//   - For a 137KB Go file where an edit near the top forces near-total GLR
//     re-derivation of the rest of the file (see the sibling repro file:
//     non-leaf subtree reuse remains unavailable for Go's top-level
//     declaration list even after campaign O(edit) W1's fragility-gated
//     admission replaced the old languageAllowsForestTopLevelSiblingReuse
//     CSS/cmake allowlist -- W1 found a deeper reduce-chain-settling gap
//     blocks it), the cost-comparison machinery
//     runs many times against large reused/live subtrees. With the small
//     128-entry cache this thrashes constantly, degrading nearly every
//     comparison to an O(depth) or worse uncached recursive walk
//     (cNodeErrorCostLang/cNodeVisibleSubtreeCountUncachedLang's recursive
//     fallback, taken whenever len(p.cNodeMemoCache) == 0 OR the 2-way set
//     evicts before the next lookup). With the 16384-entry cache the same
//     comparisons are ~O(1) amortized.
//
// Result: for the IDENTICAL file and IDENTICAL edit, whether THIS parse pays
// the slow uncached path or the fast cached path depends entirely on
// UNRELATED PRIOR HISTORY of the Parser instance used -- which any harness
// that pools/reuses Parsers (or that happens to warm one instance via an
// earlier call in the same process) will nondeterministically vary run to
// run. A harness that always uses a genuinely fresh, never-before-used
// Parser (as TestReproInsertRetryInstability in the sibling repro file does)
// will ALWAYS see the slow/cold path and therefore NOT observe the
// bimodality -- which is exactly what this repro found (consistently
// ~1.2-1.9s across 25 fresh-parser iterations, no 97ms-class outliers).
//
// Below, TestReproOrganicWarmVsCold and TestReproOrganicDeleteWarmVsCold
// demonstrate the effect directly: "WARM" primes the Parser instance with
// one throwaway parse of deliberately broken Go source (forcing real error
// recovery) before parsing/editing the real 137KB file; "COLD" does not.
// Everything else (grammars.GoLanguage(), the file, the edit offset, the
// point math) is identical between the two arms.
//
// Measured on this box (linux/amd64, 20 vCPU), insert at offset 200:
//	COLD: ~1.45s - 1.87s   (8 iterations)
//	WARM: ~0.16s - 0.22s   (8 iterations)   -- ~7.5-9x faster
// and delete at the same offset:
//	DEL-COLD: ~0.95s - 1.13s
//	DEL-WARM: ~0.15s - 0.18s               -- ~6-7x faster
//
// Both INSERT and DELETE show the SAME order-of-magnitude COLD/WARM split at
// the same offset, with AcceptedErrorRetryAttempts == 0 and identical
// tokensConsumed/maxStacksSeen in every arm. This is strong evidence that the
// reporter's observed insert-vs-delete asymmetry (insert ~24x slower than
// delete on their box, despite delete allocating MORE nodes) is plausibly
// the SAME cache warm/cold artifact landing on the insert and delete
// benchmarks differently (e.g. via harness/Parser-pool call order), rather
// than an intrinsic cost difference between insert and delete edits.
//
// A CPU profile of the WARM run (TestReproOrganicWarmCPUProfile) confirms
// the mechanism directly: cNodeErrorCost drops from 42.5% flat / 0.54s in
// the cold profile to 3.2% flat / 10ms in the warm profile, and total wall
// time drops from ~1.2s to ~0.3s, with time now spread sanely across
// ordinary lexer/shift/reduce/GSS work.
//
// All tests here are gated behind explicit environment variables so they
// never run as part of `go test ./...`.

import (
	"fmt"
	"os"
	"runtime/pprof"
	"testing"
	"time"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// primingBadGoSource is small, genuinely broken Go source used to force a
// Parser instance into real C-style error recovery (crecoveryEnteredErrorState),
// simulating an unrelated PRIOR parse (e.g. of some other file) on a
// pooled/reused Parser instance.
const primingBadGoSource = `package p

func broken( {{{ ]]] +++ ---
	if x := ; then {
		return }}} +++
	}
}

var y = )))(((;
`

// TestReproOrganicWarmVsCold is the primary nondeterminism demonstration:
// see the file-level doc comment above.
func TestReproOrganicWarmVsCold(t *testing.T) {
	if os.Getenv("GTS_REPRO_ORGANIC") == "" {
		t.Skip("set GTS_REPRO_ORGANIC=1 to run")
	}
	src, err := os.ReadFile(os.Getenv("GTS_REPRO_FILE"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lang := grammars.GoLanguage()
	insertOffset := issue380EnvInt("GTS_REPRO_OFFSET", 200)
	iters := issue380EnvInt("GTS_REPRO_ITERS", 8)
	issue380ValidOffset(t, src, insertOffset, 0)

	runOnce := func(label string, prime bool, i int) {
		parser := gotreesitter.NewParser(lang)
		if prime {
			primeTree, _ := parser.Parse([]byte(primingBadGoSource))
			if primeTree != nil {
				primeTree.Release()
			}
		}
		oldTree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("[%s %d] base parse error: %v", label, i, err)
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
			t.Fatalf("[%s %d] incremental parse error: %v", label, i, err)
		}
		fmt.Printf("%s iter=%d elapsed=%s acceptedErrAttempts=%d maxStacksSeen=%d tokensConsumed=%d\n",
			label, i, elapsed, profile.AcceptedErrorRetryAttempts, profile.MaxStacksSeen, profile.TokensConsumed)
		newTree.Release()
		oldTree.Release()
	}

	for i := 0; i < iters; i++ {
		runOnce("COLD", false, i)
	}
	for i := 0; i < iters; i++ {
		runOnce("WARM", true, i)
	}
}

// TestReproOrganicDeleteWarmVsCold is the DELETE counterpart, same offset,
// same warm/cold priming methodology.
func TestReproOrganicDeleteWarmVsCold(t *testing.T) {
	if os.Getenv("GTS_REPRO_ORGANIC_DEL") == "" {
		t.Skip("set GTS_REPRO_ORGANIC_DEL=1 to run")
	}
	src, err := os.ReadFile(os.Getenv("GTS_REPRO_FILE"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lang := grammars.GoLanguage()
	deleteOffset := issue380EnvInt("GTS_REPRO_OFFSET", 200)
	iters := issue380EnvInt("GTS_REPRO_ITERS", 4)
	issue380ValidOffset(t, src, deleteOffset, 1)

	runOnce := func(label string, prime bool, i int) {
		parser := gotreesitter.NewParser(lang)
		if prime {
			primeTree, _ := parser.Parse([]byte(primingBadGoSource))
			if primeTree != nil {
				primeTree.Release()
			}
		}
		oldTree, err := parser.Parse(src)
		if err != nil {
			t.Fatalf("[%s %d] base parse error: %v", label, i, err)
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
			t.Fatalf("[%s %d] incremental parse error: %v", label, i, err)
		}
		fmt.Printf("DEL-%s iter=%d elapsed=%s acceptedErrAttempts=%d maxStacksSeen=%d tokensConsumed=%d\n",
			label, i, elapsed, profile.AcceptedErrorRetryAttempts, profile.MaxStacksSeen, profile.TokensConsumed)
		newTree.Release()
		oldTree.Release()
	}

	for i := 0; i < iters; i++ {
		runOnce("COLD", false, i)
	}
	for i := 0; i < iters; i++ {
		runOnce("WARM", true, i)
	}
}

// TestReproOrganicWarmCPUProfile captures a CPU profile of a WARM (primed
// cache) run for direct comparison against TestReproCPUProfile's cold
// profile: cNodeErrorCost should all but vanish from the top of the profile.
func TestReproOrganicWarmCPUProfile(t *testing.T) {
	if os.Getenv("GTS_REPRO_WARM_PROFILE") == "" {
		t.Skip("set GTS_REPRO_WARM_PROFILE=1 to run")
	}
	src, err := os.ReadFile(os.Getenv("GTS_REPRO_FILE"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lang := grammars.GoLanguage()
	insertOffset := issue380EnvInt("GTS_REPRO_OFFSET", 200)
	issue380ValidOffset(t, src, insertOffset, 0)

	parser := gotreesitter.NewParser(lang)
	primeTree, _ := parser.Parse([]byte(primingBadGoSource))
	if primeTree != nil {
		primeTree.Release()
	}
	oldTree, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("base parse error: %v", err)
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

	out := os.Getenv("GTS_REPRO_PROFILE_OUT")
	if out == "" {
		out = "cpu_warm.prof"
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
	fmt.Printf("WARM elapsed=%s acceptedErrAttempts=%d maxStacksSeen=%d tokensConsumed=%d\n",
		elapsed, profile.AcceptedErrorRetryAttempts, profile.MaxStacksSeen, profile.TokensConsumed)
	newTree.Release()
	oldTree.Release()
}
