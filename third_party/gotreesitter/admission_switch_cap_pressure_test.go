//go:build !gts_no_parsercorephase0

package gotreesitter_test

import (
	"bytes"
	"strings"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// TestAdmissionSwitchProjectsNodeCapBeforeArenaExhaustion proves the admission
// runner stops a stable cap-bound parse before the compact core fills its node
// arena. The production fallback remains responsible for the public result.
func TestAdmissionSwitchProjectsNodeCapBeforeArenaExhaustion(t *testing.T) {
	t.Setenv("GOT_PARSE_MEMORY_BUDGET_MB", "512")
	gts.ResetParseEnvConfigCacheForTests()
	t.Cleanup(gts.ResetParseEnvConfigCacheForTests)

	var source bytes.Buffer
	source.WriteString("package p\n\nfunc f() {\n")
	for range 120000 {
		source.WriteString("\t_ = 1\n")
	}
	source.WriteString("}\n")

	parser := gts.NewParser(grammars.GoLanguage())
	tree, ok, reason := gts.TryCompactFullParseRouteForTest(parser, source.Bytes())
	if tree != nil {
		tree.Release()
	}
	if ok {
		t.Fatal("compact route accepted the cap-pressure witness")
	}
	if !strings.Contains(reason, "scheduler projected node arena cap") {
		t.Fatalf("decline reason = %q, want the projected node-cap stop", reason)
	}
	if got := gts.AdmissionCandidateCompactFootprintBytesForTest(parser); got > 64<<20 {
		t.Fatalf("compact footprint after decline = %d bytes, want at most 64 MiB", got)
	}
}
