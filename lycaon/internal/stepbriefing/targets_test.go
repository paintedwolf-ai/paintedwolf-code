package stepbriefing

import (
	"slices"
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func ptr(value string) *string { return &value }

func effect(id string, ordinal int64, opts ...func(*wire.SourceWalkEffect)) wire.SourceWalkEffect {
	e := wire.SourceWalkEffect{ID: id, Ordinal: ordinal, RootID: "root", FileID: "file-" + id, AfterVersionID: "v-" + id,
		Contributors: []wire.SourceContributor{{Origin: "agent", SessionID: "s"}}}
	for _, opt := range opts {
		opt(&e)
	}
	return e
}

func outside(e *wire.SourceWalkEffect) {
	e.Contributors = []wire.SourceContributor{{Origin: "external"}}
}

func keys(targets map[string]wire.SourceStepBriefingTarget) []string {
	out := make([]string, 0, len(targets))
	for key := range targets {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

func TestTargetsKeyStepsAsTheRailDoes(t *testing.T) {
	page := wire.SourceWalkResponse{
		GitChanges: []wire.SourceGitChange{{ID: "g1", Ordinal: 5, RootID: "root"}},
		Commands:   []wire.SourceCommandWindow{{ID: "c1", Ordinal: 3}, {ID: "idle", Ordinal: 4}},
		Files: []wire.SourceWalkFile{{Effects: []wire.SourceWalkEffect{
			effect("in-git", 6, func(e *wire.SourceWalkEffect) { e.GitChangeID = ptr("g1"); e.CommandID = ptr("c1") }),
			effect("in-command", 3, func(e *wire.SourceWalkEffect) { e.CommandID = ptr("c1") }),
			effect("single", 1),
			effect("out-a", 10, outside), effect("out-b", 11, outside),
			effect("between", 15),
			effect("lone-outside", 20, outside),
		}}},
	}
	targets := Targets(page)
	want := []string{"between", "command:c1", "git:g1", "lone-outside", "outside:out-a", "single"}
	if got := keys(targets); !slices.Equal(got, want) {
		t.Fatalf("keys %v; want %v", got, want)
	}
	if group := targets["outside:out-a"]; group.Kind != "outside" || !slices.Equal(group.EffectIds, []string{"out-a", "out-b"}) {
		t.Fatalf("outside group %+v", group)
	}
	if single := targets["single"]; single.Kind != "effect" || single.Scope.FileID != "file-single" || single.ScopeFingerprint == "" {
		t.Fatalf("effect target %+v", single)
	}
}

func TestGrowingOutsideGroupsGetNewFingerprints(t *testing.T) {
	page := func(n int) wire.SourceWalkResponse {
		var effects []wire.SourceWalkEffect
		for i := range n {
			effects = append(effects, effect(string(rune('a'+i)), int64(i), outside))
		}
		return wire.SourceWalkResponse{Files: []wire.SourceWalkFile{{Effects: effects}}}
	}
	two, three := Targets(page(2))["outside:a"], Targets(page(3))["outside:a"]
	if two.ScopeFingerprint == three.ScopeFingerprint {
		t.Fatal("a grown outside group kept its frozen identity")
	}
}
