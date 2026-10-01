package gotreesitter_test

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// TestSQLIncrementalScannerCertification is the committed witness for issues
// #430 and #432. Ordinary CI crosses three length-changing edit routes at the
// start, middle, and end of small clean and recovered SQL corpora, then keeps
// one representative 137 KiB clean-corpus bound. This certifies checkpoint
// admission without making every race shard pay the malformed recovery cost at
// macro sizes. GTS_SQL_SCANNER_SLOW_TIER=1 adds the exhaustive 20 KiB matrix,
// a 137 KiB malformed witness, and the 1 MiB catastrophe lane.
func TestSQLIncrementalScannerCertification(t *testing.T) {
	plans := []sqlCertificationPlan{
		{
			name:      "4KiB",
			size:      4 << 10,
			malformed: []bool{false, true},
			positions: []string{"start", "middle", "end"},
			classes:   []sqlCertificationEditClass{sqlCertificationInsert, sqlCertificationDelete, sqlCertificationReplace},
		},
		{
			name:      "137KiB",
			size:      137 << 10,
			malformed: []bool{false},
			positions: []string{"middle"},
			classes:   []sqlCertificationEditClass{sqlCertificationReplace},
		},
	}
	if strings.TrimSpace(os.Getenv("GTS_SQL_SCANNER_SLOW_TIER")) != "" {
		plans = append(plans,
			sqlCertificationPlan{
				name:      "20KiB-slow",
				size:      20 << 10,
				malformed: []bool{false, true},
				positions: []string{"start", "middle", "end"},
				classes:   []sqlCertificationEditClass{sqlCertificationInsert, sqlCertificationDelete, sqlCertificationReplace},
			},
			sqlCertificationPlan{
				name:      "137KiB-malformed-slow",
				size:      137 << 10,
				malformed: []bool{true},
				positions: []string{"middle"},
				classes:   []sqlCertificationEditClass{sqlCertificationReplace},
			},
			sqlCertificationPlan{
				name:      "1MiB-slow",
				size:      1 << 20,
				malformed: []bool{false},
				positions: []string{"middle"},
				classes:   []sqlCertificationEditClass{sqlCertificationReplace},
			},
		)
	}

	for _, plan := range plans {
		for _, malformed := range plan.malformed {
			source, sites := makeSQLScannerCertificationSource(plan.size, malformed)
			shape := "clean"
			if malformed {
				shape = "malformed"
			}
			for _, position := range plan.positions {
				var site int
				switch position {
				case "start":
					site = sites[1]
				case "middle":
					site = sites[len(sites)/2]
				case "end":
					site = sites[len(sites)-2]
				default:
					t.Fatalf("unknown SQL certification position %q", position)
				}
				for _, class := range plan.classes {
					name := fmt.Sprintf("%s/%s/%s/%s", plan.name, shape, class, position)
					t.Run(name, func(t *testing.T) {
						edited, edit := applySQLCertificationEdit(source, site, class)
						first := runSQLCertificationSample(t, source, edited, edit, malformed)
						second := runSQLCertificationSample(t, source, edited, edit, malformed)
						checkSQLCertificationBounds(t, len(edited), first)
						checkSQLCertificationBounds(t, len(edited), second)
						firstStable := first
						secondStable := second
						// Arena pools may retain different capacity from unrelated prior
						// parses. Memory is deterministically source-bounded below; causal
						// work and reuse counters must be exactly repeatable.
						firstStable.ArenaBytesAllocated, firstStable.ScratchBytesAllocated = 0, 0
						secondStable.ArenaBytesAllocated, secondStable.ScratchBytesAllocated = 0, 0
						if firstStable != secondStable {
							t.Fatalf("non-deterministic SQL incremental counters:\n first=%+v\n second=%+v", first, second)
						}
						t.Logf("bytes=%d reused=%d (%.1f%%) subtrees=%d tokens=%d nodes=%d scannerRejects=%d memory=%d",
							len(edited), first.ReusedBytes, 100*float64(first.ReusedBytes)/float64(len(edited)),
							first.ReusedSubtrees, first.TokensConsumed, first.NewNodesAllocated,
							first.ReuseRejectScannerUnquiescent, first.ArenaBytesAllocated+first.ScratchBytesAllocated)
					})
				}
			}
		}
	}
}

type sqlCertificationPlan struct {
	name      string
	size      int
	malformed []bool
	positions []string
	classes   []sqlCertificationEditClass
}

type sqlCertificationEditClass uint8

const (
	sqlCertificationInsert sqlCertificationEditClass = iota
	sqlCertificationDelete
	sqlCertificationReplace
)

func (c sqlCertificationEditClass) String() string {
	switch c {
	case sqlCertificationInsert:
		return "insert"
	case sqlCertificationDelete:
		return "delete"
	case sqlCertificationReplace:
		return "replace"
	default:
		return "unknown"
	}
}

func makeSQLScannerCertificationSource(target int, malformed bool) ([]byte, []int) {
	var source strings.Builder
	source.Grow(target + 128)
	sites := make([]int, 0, target/40)
	for i := 0; source.Len() < target || len(sites) < 4; i++ {
		prefix := "SELECT $q$payload_"
		source.WriteString(prefix)
		sites = append(sites, source.Len())
		fmt.Fprintf(&source, "%06d$q$ AS ", i)
		if malformed {
			// A missing right operand keeps the parser on a recovered,
			// full-span tree while the dollar-quote scanner still opens,
			// carries, serializes, and closes its real tag state.
			fmt.Fprintf(&source, "value_%06d + ;\n", i)
		} else {
			fmt.Fprintf(&source, "value_%06d;\n", i)
		}
	}
	return []byte(source.String()), sites
}

func applySQLCertificationEdit(source []byte, offset int, class sqlCertificationEditClass) ([]byte, gts.InputEdit) {
	oldEnd := offset
	replacement := []byte{'9'}
	switch class {
	case sqlCertificationDelete:
		oldEnd = offset + 1
		replacement = nil
	case sqlCertificationReplace:
		oldEnd = offset + 1
		// A real changed-length replacement, distinct from both insertion and
		// deletion and from the token-invariant 1->1 substitution fast path.
		replacement = []byte("88")
	}
	edited := make([]byte, 0, len(source)-(oldEnd-offset)+len(replacement))
	edited = append(edited, source[:offset]...)
	edited = append(edited, replacement...)
	edited = append(edited, source[oldEnd:]...)
	return edited, gts.InputEdit{
		StartByte:   uint32(offset),
		OldEndByte:  uint32(oldEnd),
		NewEndByte:  uint32(offset + len(replacement)),
		StartPoint:  pointForOffset(source, offset),
		OldEndPoint: pointForOffset(source, oldEnd),
		NewEndPoint: pointForOffset(edited, offset+len(replacement)),
	}
}

type sqlCertificationCounters struct {
	ReuseUnsupported              bool
	OldTreeReuseRoute             bool
	ReusedSubtrees                uint64
	ReusedBytes                   uint64
	NewNodesAllocated             uint64
	TokensConsumed                uint64
	SingleStackIterations         int
	MultiStackIterations          int
	MaxStacksSeen                 int
	ReuseRejectScannerUnquiescent uint64
	ArenaBytesAllocated           int64
	ScratchBytesAllocated         int64
}

func runSQLCertificationSample(t *testing.T, source, edited []byte, edit gts.InputEdit, malformed bool) sqlCertificationCounters {
	t.Helper()
	lang := grammars.SqlLanguage()
	parser := gts.NewParser(lang)
	parser.SetAdmissionCandidateRoute(false)
	oldTree, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("initial SQL parse: %v", err)
	}
	defer oldTree.Release()
	requireCompleteParse(t, oldTree, source, lang, "initial SQL certification")
	requireSQLAcceptedEOF(t, oldTree, source, "initial SQL certification")
	if oldTree.RootNode().HasError() != malformed {
		t.Fatalf("initial SQL error state = %v, want malformed=%v", oldTree.RootNode().HasError(), malformed)
	}

	oldTree.Edit(edit)
	incremental, profile, err := parser.ParseIncrementalProfiled(edited, oldTree)
	if err != nil {
		t.Fatalf("incremental SQL parse: %v", err)
	}
	defer incremental.Release()
	requireCompleteParse(t, incremental, edited, lang, "incremental SQL certification")
	requireSQLAcceptedEOF(t, incremental, edited, "incremental SQL certification")
	if profile.ReuseUnsupported {
		t.Fatalf("certified SQL scanner fell back: reason=%q", profile.ReuseUnsupportedReason)
	}
	if profile.ReusedSubtrees == 0 || profile.ReusedBytes == 0 {
		t.Fatalf("certified SQL scanner did not reuse old syntax: %+v", profile)
	}
	if !profile.OldTreeReuseRoute {
		t.Fatalf("certified SQL scanner did not enter the old-tree reuse route: %+v", profile)
	}
	if incremental.RootNode().HasError() != malformed {
		t.Fatalf("incremental SQL error state = %v, want malformed=%v", incremental.RootNode().HasError(), malformed)
	}

	freshParser := gts.NewParser(lang)
	freshParser.SetAdmissionCandidateRoute(false)
	fresh, err := freshParser.Parse(edited)
	if err != nil {
		t.Fatalf("fresh SQL parse: %v", err)
	}
	defer fresh.Release()
	requireCompleteParse(t, fresh, edited, lang, "fresh SQL certification")
	requireSQLAcceptedEOF(t, fresh, edited, "fresh SQL certification")
	requireIncrementalDeepTreeMatchesFresh(t, incremental, fresh, lang)

	return sqlCertificationCounters{
		ReuseUnsupported:              profile.ReuseUnsupported,
		OldTreeReuseRoute:             profile.OldTreeReuseRoute,
		ReusedSubtrees:                profile.ReusedSubtrees,
		ReusedBytes:                   profile.ReusedBytes,
		NewNodesAllocated:             profile.NewNodesAllocated,
		TokensConsumed:                profile.TokensConsumed,
		SingleStackIterations:         profile.SingleStackIterations,
		MultiStackIterations:          profile.MultiStackIterations,
		MaxStacksSeen:                 profile.MaxStacksSeen,
		ReuseRejectScannerUnquiescent: profile.ReuseRejectScannerUnquiescent,
		ArenaBytesAllocated:           profile.ArenaBytesAllocated,
		ScratchBytesAllocated:         profile.ScratchBytesAllocated,
	}
}

func checkSQLCertificationBounds(t *testing.T, editedLen int, counters sqlCertificationCounters) {
	t.Helper()
	kib := uint64((editedLen + 1023) / 1024)
	// These remain source-linear catastrophe bounds. They intentionally do not
	// claim O(edit): the 1 MiB certification currently materializes substantial
	// old-tree/checkpoint state and is tracked as an open performance residual.
	maxTokens := uint64(1024) + kib*384
	maxNodes := uint64(4096) + kib*2200
	maxIterations := 1024 + int(kib)*768
	maxMemory := int64(64<<20) + int64(kib)*(128<<10)
	iterations := counters.SingleStackIterations + counters.MultiStackIterations
	memory := counters.ArenaBytesAllocated + counters.ScratchBytesAllocated
	if counters.ReusedBytes*100 < uint64(editedLen)*25 {
		t.Fatalf("SQL certification reused only %d/%d bytes (<25%%); checkpoint admission/work ratchet regressed: %+v",
			counters.ReusedBytes, editedLen, counters)
	}
	if counters.TokensConsumed > maxTokens || counters.NewNodesAllocated > maxNodes || iterations > maxIterations || memory > maxMemory {
		t.Fatalf("SQL certification resource ceiling exceeded: tokens=%d/%d nodes=%d/%d iterations=%d/%d memory=%d/%d counters=%+v",
			counters.TokensConsumed, maxTokens, counters.NewNodesAllocated, maxNodes,
			iterations, maxIterations, memory, maxMemory, counters)
	}
}

func requireSQLAcceptedEOF(t *testing.T, tree *gts.Tree, source []byte, phase string) {
	t.Helper()
	if tree == nil {
		t.Fatalf("%s: nil tree", phase)
	}
	rt := tree.ParseRuntime()
	if tree.ParseStoppedEarly() || rt.StopReason != gts.ParseStopAccepted || rt.Truncated ||
		!rt.LastTokenWasEOF || rt.LastTokenEndByte != uint32(len(source)) || rt.ExpectedEOFByte != uint32(len(source)) {
		t.Fatalf("%s: parse was not accepted, non-truncated, and EOF-complete: %s", phase, rt.Summary())
	}
}

// TestSQLLongDollarTagPreservesFullParseSemantics proves checkpoint capacity is
// an incremental-admission concern, not a SQL language restriction. The valid
// delimiter exceeds the runtime checkpoint buffer, still parses to accepted
// EOF, and causes affected incremental candidates to fail closed while the
// returned tree remains fresh-equal. This is also the canonical full-pipeline
// liveness receipt for ReuseRejectScannerUnquiescent: unlike ordinary
// checkpoint languages whose candidates may become fully aligned as scheduler
// ownership improves, this fixture necessarily exceeds checkpoint capacity.
func TestSQLLongDollarTagPreservesFullParseSemantics(t *testing.T) {
	tag := "$" + strings.Repeat("long_tag_", 520) + "$"
	source := []byte("SELECT " + tag + "payload" + tag + " AS body;\nSELECT 1;\n")
	lang := grammars.SqlLanguage()
	parser := gts.NewParser(lang)
	parser.SetAdmissionCandidateRoute(false)
	oldTree, err := parser.Parse(source)
	if err != nil {
		t.Fatalf("long-tag full parse: %v", err)
	}
	defer oldTree.Release()
	requireCompleteParse(t, oldTree, source, lang, "long-tag full parse")
	requireSQLAcceptedEOF(t, oldTree, source, "long-tag full parse")
	if oldTree.RootNode().HasError() {
		t.Fatalf("valid long dollar tag produced ERROR: %s", oldTree.RootNode().SExpr(lang))
	}

	offset := bytes.Index(source, []byte("payload")) + 3
	edited, edit := applySQLCertificationEdit(source, offset, sqlCertificationReplace)
	oldTree.Edit(edit)
	incremental, profile, err := parser.ParseIncrementalProfiled(edited, oldTree)
	if err != nil {
		t.Fatalf("long-tag incremental parse: %v", err)
	}
	defer incremental.Release()
	requireCompleteParse(t, incremental, edited, lang, "long-tag incremental parse")
	requireSQLAcceptedEOF(t, incremental, edited, "long-tag incremental parse")
	if profile.ReuseRejectScannerUnquiescent == 0 {
		t.Fatalf("uncheckpointable long-tag state did not fail closed at any reuse boundary: %+v", profile)
	}

	freshParser := gts.NewParser(lang)
	freshParser.SetAdmissionCandidateRoute(false)
	fresh, err := freshParser.Parse(edited)
	if err != nil {
		t.Fatalf("long-tag fresh parse: %v", err)
	}
	defer fresh.Release()
	requireCompleteParse(t, fresh, edited, lang, "long-tag fresh parse")
	requireSQLAcceptedEOF(t, fresh, edited, "long-tag fresh parse")
	requireIncrementalDeepTreeMatchesFresh(t, incremental, fresh, lang)
}
