package touchedpaths_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/touchedpaths"
)

type stubLedger struct {
	ev evidence.Ledger
}

func (s stubLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return s.ev, nil
}

type stubAuthored struct {
	byRoot map[string][]string
}

func (s stubAuthored) SessionAuthoredPaths(_ context.Context, _, _, rootID string) ([]string, error) {
	return s.byRoot[rootID], nil
}

func TestListDeduplicatesAcrossRootsAndOrdersAuthoredBeforeNewestObserved(t *testing.T) {
	roots := []projectroot.RootRef{
		{ID: "r1", Label: "app", Path: "/ws/app", IsPrimary: true},
		{ID: "r2", Label: "docs", Path: "/ws/docs"},
		{ID: "r-ext", Label: "ext", Path: "/ext", External: true},
	}

	authored := stubAuthored{
		byRoot: map[string][]string{
			"r1":    {"z.go", "./a.go", "a.go", "../escape.go"},
			"r2":    {"guide.md"},
			"r-ext": {"secret.txt"},
		},
	}

	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", RootID: "r1", Path: "old.go"},
		{Handle: "grep#1", Kind: "grep", RootID: "r2", Path: "guide.md"},
		{Handle: "read#2", Kind: "read", RootID: "r1", Path: "superseded.go", SupersededBy: "read#3"},
		{Handle: "verify#1", GateType: "verify", RootID: "r1", Path: "gate.go"},
		{Handle: "command#1", Kind: "command", Path: "@docs/extra.md"},
		{Handle: "grep#2", Kind: "grep", RootID: "r1", Path: "newest.go"},
	})
	// Secondary-root ByPath fallback entries share canonical identity with rooted rows.
	ev.ByPath["@docs/guide.md"] = []string{"orphan#9"}
	ev.ByPath["@docs/fallback.md"] = []string{"orphan#4"}

	got := touchedpaths.List(
		context.Background(),
		"proj-1",
		"sess-1",
		"r1",
		roots,
		stubLedger{ev: ev},
		authored,
		touchedpaths.MaxCandidates,
	)

	want := []touchedpaths.Path{
		{RootID: "r1", RelPath: "a.go"},
		{RootID: "r1", RelPath: "z.go"},
		{RootID: "r2", RelPath: "guide.md"},
		{RootID: "r1", RelPath: "newest.go"},
		{RootID: "r2", RelPath: "extra.md"},
		{RootID: "r1", RelPath: "old.go"},
		{RootID: "r2", RelPath: "fallback.md"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() =\n  %#v\nwant\n  %#v", got, want)
	}
}

func TestListAppliesCapAfterDeduplication(t *testing.T) {
	roots := []projectroot.RootRef{
		{ID: "r1", Label: "app", Path: "/ws/app", IsPrimary: true},
	}

	var authoredPaths []string
	var records []evidence.Record
	ordinal := 1
	for i := range 40 {
		p := fmt.Sprintf("authored/f%02d.go", i)
		authoredPaths = append(authoredPaths, p)
		records = append(records, evidence.Record{
			Handle: fmt.Sprintf("read#%d", ordinal),
			Kind:   "read",
			RootID: "r1",
			Path:   p,
		})
		ordinal++
	}
	for i := range 40 {
		records = append(records, evidence.Record{
			Handle: fmt.Sprintf("read#%d", ordinal),
			Kind:   "read",
			RootID: "r1",
			Path:   fmt.Sprintf("observed/o%02d.go", i),
		})
		ordinal++
	}

	ledger := stubLedger{ev: evidence.AssembleLedger(records)}
	authored := stubAuthored{byRoot: map[string][]string{"r1": authoredPaths}}

	got := touchedpaths.List(
		context.Background(),
		"proj-1",
		"sess-1",
		"r1",
		roots,
		ledger,
		authored,
		touchedpaths.MaxCandidates,
	)

	if len(got) != touchedpaths.MaxCandidates {
		t.Fatalf("len(got) = %d, want %d", len(got), touchedpaths.MaxCandidates)
	}
	for i := range 40 {
		want := touchedpaths.Path{RootID: "r1", RelPath: fmt.Sprintf("authored/f%02d.go", i)}
		if got[i] != want {
			t.Fatalf("got[%d] = %+v, want %+v", i, got[i], want)
		}
	}
	for i := range 24 {
		want := touchedpaths.Path{RootID: "r1", RelPath: fmt.Sprintf("observed/o%02d.go", 39-i)}
		if got[40+i] != want {
			t.Fatalf("got[%d] = %+v, want %+v", 40+i, got[40+i], want)
		}
	}

	uncapped := touchedpaths.List(
		context.Background(),
		"proj-1",
		"sess-1",
		"r1",
		roots,
		ledger,
		authored,
		0,
	)
	if len(uncapped) != 80 {
		t.Fatalf("len(uncapped) = %d, want 80", len(uncapped))
	}
}
