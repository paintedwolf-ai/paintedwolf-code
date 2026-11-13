package toolvocab_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

// Handles are checked against the kinds the evidence catalog says each
// reachable tool mints; the test reads those kinds rather than naming them.
func TestUnreachableNamesReportsHandlesNoReachableToolMints(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "evidence binding", err)
	held := binding.ToolKind("terminal_snapshot")
	sealed := binding.ToolKindForArgs("command", map[string]any{"terminal_capture": map[string]any{}})
	if held == "" || sealed == "" || held == sealed {
		t.Fatalf("fixture kinds: held %q, sealed %q", held, sealed)
	}
	corpus := "Cite `" + held + "#` or `" + sealed + "#4`, or a worker's `[leg-a:" + sealed + "#2]`."

	got := toolvocab.UnreachableNames(catalog, corpus, map[string]bool{"terminal_snapshot": true})
	if !slices.Contains(got, sealed+"#") || slices.Contains(got, held+"#") {
		t.Fatalf("terminal_snapshot alone: %v; want only %q unmintable", got, sealed+"#")
	}
	got = toolvocab.UnreachableNames(catalog, corpus, map[string]bool{"terminal_snapshot": true, "command": true})
	if slices.Contains(got, sealed+"#") || slices.Contains(got, held+"#") {
		t.Fatalf("with command: %v; want both kinds mintable", got)
	}
}

func TestUnreachableNamesReportsOperationKindsOnlyThroughCommand(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "evidence binding", err)
	kind := binding.ToolKind("read")
	if kind == "" {
		t.Fatal("read mints no kind")
	}
	corpus := "A receipt such as `" + kind + "#3`."
	if got := toolvocab.UnreachableNames(catalog, corpus, map[string]bool{"recall": true}); !slices.Contains(got, kind+"#") {
		t.Fatalf("without command: %v", got)
	}
	if got := toolvocab.UnreachableNames(catalog, corpus, map[string]bool{"recall": true, "command": true}); slices.Contains(got, kind+"#") {
		t.Fatalf("with command: %v", got)
	}
}

func TestUnreachableNamesReportsToolNamesUsedAsHandles(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	binding, err := evidence.LoadBinding()
	testutil.FailErr(t, "evidence binding", err)
	if binding.KindShape("terminal_open") != "" {
		t.Fatal("terminal_open is an evidence kind; pick a tool that is not")
	}
	got := toolvocab.UnreachableNames(catalog, "Cite `terminal_open#1`.", map[string]bool{"terminal_open": true})
	if !slices.Contains(got, "terminal_open#") {
		t.Fatalf("tool used as handle: %v", got)
	}
}

func TestUnreachableNamesReportsAHandleRenderedWithoutItsFact(t *testing.T) {
	catalog, err := toolvocab.NewCatalog(&toolschema.Config{}, readOnlyProfiles(), toolvocab.Provenance{})
	testutil.FailErr(t, "catalog", err)
	if got := toolvocab.UnreachableNames(catalog, "its `#` handle proves only that it ran", map[string]bool{"recall": true}); !slices.Contains(got, "#") {
		t.Fatalf("empty handle: %v", got)
	}
}
