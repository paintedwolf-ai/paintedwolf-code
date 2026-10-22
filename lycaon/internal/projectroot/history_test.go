package projectroot

import (
	"fmt"
	"testing"
)

func TestDiffRootsClassifiesByIdentity(t *testing.T) {
	api := RootRef{ID: "a", Label: "api", Path: "/abs/api", IsPrimary: true}
	web := RootRef{ID: "w", Label: "web", Path: "/abs/web"}
	moved, renamed := api, web
	moved.Path, renamed.Label = "/abs/api-2", "site"
	docs := RootRef{ID: "d", Label: "docs", Path: "/abs/docs"}
	change := DiffRoots([]RootRef{api, web}, []RootRef{moved, renamed, docs})
	if len(change.Added) != 1 || change.Added[0].ID != "d" {
		t.Fatalf("added = %+v", change.Added)
	}
	if len(change.Retired) != 2 || change.Retired[0].Reason != RootMoved || change.Retired[0].Successor.Path != "/abs/api-2" || change.Retired[1].Reason != RootRenamed || change.Retired[1].Successor.Label != "site" {
		t.Fatalf("retired = %+v", change.Retired)
	}
	removed := DiffRoots([]RootRef{api, web}, []RootRef{api})
	if len(removed.Retired) != 1 || removed.Retired[0].Reason != RootDetached || removed.Retired[0].Root.ID != "w" || removed.Empty() {
		t.Fatalf("removed = %+v", removed)
	}
	if !DiffRoots([]RootRef{api, web}, []RootRef{web, api}).Empty() {
		t.Fatal("reordering the same roots announced a change")
	}
	webPrimary, apiPlain := web, api
	webPrimary.IsPrimary, apiPlain.IsPrimary = true, false
	if primary := DiffRoots([]RootRef{api, web}, []RootRef{apiPlain, webPrimary}); primary.Primary.ID != "w" || len(primary.Retired) != 0 {
		t.Fatalf("primary change = %+v", primary)
	}
}

func TestHistoryAnswersStaleSpellingsFromRecordedGenerations(t *testing.T) {
	var history History
	api := RootRef{ID: "a", Label: "api", Path: "/abs/api", IsPrimary: true}
	web := RootRef{ID: "w", Label: "web", Path: "/abs/web"}
	before := history.View("p")
	if _, changed := history.Record("p", []RootRef{api, web}, []RootRef{api, web}); changed {
		t.Fatal("an unchanged root set advanced the generation")
	}
	change, changed := history.Record("p", []RootRef{api, web}, []RootRef{api})
	if !changed || change.Generation != 1 || history.View("p").Generation != 1 || history.View("other").Generation != 0 {
		t.Fatalf("change=%+v views=%d,%d", change, history.View("p").Generation, history.View("other").Generation)
	}
	current := []RootRef{api}
	after := history.View("p")
	if r, stale := after.StaleLabel(current, "WEB"); !stale || r.Reason != RootDetached || r.Root.Path != "/abs/web" {
		t.Fatalf("stale label = %+v,%v", r, stale)
	}
	if _, stale := after.StaleLabel(current, "api"); stale {
		t.Fatal("an attached label reported stale")
	}
	if _, stale := after.StaleLabel(current, "never"); stale {
		t.Fatal("a label no root ever carried reported stale")
	}
	if r, stale := after.StalePath(current, "/abs/web/src/main.go"); !stale || r.Root.ID != "w" {
		t.Fatalf("stale path = %+v,%v", r, stale)
	}
	for _, path := range []string{"/abs/api/web", "/abs/webby", "relative/web", "/abs"} {
		if _, stale := after.StalePath(current, path); stale {
			t.Fatalf("%s reported stale", path)
		}
	}
	// The pre-change view sees the removal as later than itself; the
	// post-change view does not.
	if _, retired := before.RetiredSince(web); !retired {
		t.Fatal("pre-change view kept authority over a removed root")
	}
	if _, retired := after.RetiredSince(web); retired {
		t.Fatal("post-change view reported its own baseline as a later removal")
	}
	if _, retired := before.RetiredSince(api); retired {
		t.Fatal("a kept root lost authority")
	}
	// A re-attached path at the same spelling resolves again.
	readded := RootRef{ID: "w2", Label: "web", Path: "/abs/web"}
	history.Record("p", current, []RootRef{api, readded})
	if _, stale := history.View("p").StalePath([]RootRef{api, readded}, "/abs/web/x"); stale {
		t.Fatal("a re-attached folder stayed stale")
	}
}

func TestHistoryRenameRetiresLabelButNotPathAuthority(t *testing.T) {
	var history History
	web := RootRef{ID: "w", Label: "web", Path: "/abs/web", IsPrimary: true}
	site := web
	site.Label = "site"
	before := history.View("p")
	history.Record("p", []RootRef{web}, []RootRef{site})
	view := history.View("p")
	if r, stale := view.StaleLabel([]RootRef{site}, "web"); !stale || r.Reason != RootRenamed || r.Successor.Label != "site" {
		t.Fatalf("renamed label = %+v,%v", r, stale)
	}
	if _, retired := before.RetiredSince(web); retired {
		t.Fatal("a rename revoked effects under an unchanged path")
	}
	moved := site
	moved.Path = "/abs/site"
	history.Record("p", []RootRef{site}, []RootRef{moved})
	if r, retired := before.RetiredSince(web); !retired || r.Reason != RootMoved {
		t.Fatalf("move after rename = %+v,%v", r, retired)
	}
	if r, stale := history.View("p").StalePath([]RootRef{moved}, "/abs/web/index.html"); !stale || r.Successor.Path != "/abs/site" {
		t.Fatalf("moved path = %+v,%v", r, stale)
	}
}

func TestHistoryBoundsRetiredSpellings(t *testing.T) {
	var history History
	keep := RootRef{ID: "k", Label: "keep", Path: "/abs/keep", IsPrimary: true}
	for i := 0; i < historyLimit+5; i++ {
		extra := RootRef{ID: fmt.Sprint(i), Label: fmt.Sprint("r", i), Path: fmt.Sprint("/abs/r", i)}
		history.Record("p", []RootRef{keep}, []RootRef{keep, extra})
		history.Record("p", []RootRef{keep, extra}, []RootRef{keep})
	}
	view := history.View("p")
	if _, stale := view.StaleLabel([]RootRef{keep}, "r0"); stale {
		t.Fatal("history kept more than its bound")
	}
	if _, stale := view.StaleLabel([]RootRef{keep}, fmt.Sprint("r", historyLimit+4)); !stale {
		t.Fatal("history dropped its latest retirement")
	}
	var nilHistory *History
	if change, changed := nilHistory.Record("p", []RootRef{keep}, nil); !changed || len(change.Retired) != 1 {
		t.Fatal("a nil history hid the change it was given")
	}
	if nilHistory.View("p").Generation != 0 {
		t.Fatal("a nil history reported a generation")
	}
}
