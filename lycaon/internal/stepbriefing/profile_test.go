package stepbriefing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type fakeLedger struct {
	effects map[string]sourceledger.Effect
	windows map[string]sourceledger.CommandWindow
	groups  map[string][]sourceledger.Effect
}

func (l fakeLedger) EffectsByID(_ context.Context, _ string, ids []string) ([]sourceledger.Effect, error) {
	var out []sourceledger.Effect
	for _, id := range ids {
		if effect, ok := l.effects[id]; ok {
			out = append(out, effect)
		}
	}
	return out, nil
}

func (l fakeLedger) EffectsInGroup(_ context.Context, _, window, git string) ([]sourceledger.Effect, error) {
	return l.groups[window+git], nil
}

func (l fakeLedger) CommandWindows(_ context.Context, ids []string) (map[string]sourceledger.CommandWindow, error) {
	return l.windows, nil
}

func (fakeLedger) GitTransitionsByIDs(context.Context, []string) (map[string]sourceledger.GitTransition, error) {
	return nil, nil
}

func (l fakeLedger) CompareEffect(_ context.Context, _, id string) (sourceledger.Comparison, error) {
	return sourceledger.Comparison{EffectID: id,
		Before: sourceledger.ComparisonSide{Path: "a.go", Availability: sourceledger.ContentAvailable, Content: "package a\n"},
		After:  sourceledger.ComparisonSide{Path: "a.go", Availability: sourceledger.ContentAvailable, Content: "package a\n\nfunc Refresh() {}\n"}}, nil
}

type fakeCause struct{}

func (fakeCause) ReadActionCause(context.Context, briefing.ActionRef) (briefing.CausalRead, error) {
	return briefing.CausalRead{Found: true, Intent: "Add token refresh", IntentID: "u1", Call: briefing.CallExcerpt{Name: "edit", Args: `{"path":"a.go"}`}}, nil
}

func agentEffect(id string) sourceledger.Effect {
	return sourceledger.Effect{ID: id, FileID: "f-" + id, Path: "a.go", AfterVersionID: "v-" + id, SessionID: "s", ToolCallID: "call"}
}

func target(t wire.SourceStepBriefingTarget) briefing.Target {
	raw, _ := json.Marshal(t)
	return briefing.Target{ProjectID: "p", Ref: raw}
}

func TestEffectStepAsksPurposeAndChange(t *testing.T) {
	ledger := fakeLedger{effects: map[string]sourceledger.Effect{"e1": agentEffect("e1")}}
	request, err := New(ledger, fakeCause{}).Resolve(t.Context(), target(wire.SourceStepBriefingTarget{Kind: "effect", TargetKey: "e1", ScopeFingerprint: fingerprint("e1", "v-e1"),
		Scope: wire.SourceStepBriefingScope{Kind: "file", RootIds: []string{"r"}}}))
	testutil.FailErr(t, "resolve", err)
	ids := []string{}
	for _, section := range request.Sections {
		ids = append(ids, section.ID)
	}
	if len(ids) != 3 || ids[0] != "purpose" || ids[1] != "change" || ids[2] != "outcome" {
		t.Fatalf("sections %v", ids)
	}
	if request.Sections[2].Text != "Changed 1 file" {
		t.Fatalf("outcome %q", request.Sections[2].Text)
	}
}

func TestOutsideGroupHasNoPurpose(t *testing.T) {
	ledger := fakeLedger{effects: map[string]sourceledger.Effect{"o1": {ID: "o1", FileID: "f1"}, "o2": {ID: "o2", FileID: "f2"}}}
	request, err := New(ledger, fakeCause{}).Resolve(t.Context(), target(wire.SourceStepBriefingTarget{Kind: "outside", TargetKey: "outside:o1",
		EffectIds: []string{"o1", "o2"}, ScopeFingerprint: fingerprint("outside:o1", "o1", "o2"), Scope: wire.SourceStepBriefingScope{Kind: "outside"}}))
	testutil.FailErr(t, "resolve", err)
	if request.Sections[0].ID == "purpose" {
		t.Fatal("an outside group with no session asked why it was made")
	}
}

func TestStepRefusals(t *testing.T) {
	ledger := fakeLedger{
		effects: map[string]sourceledger.Effect{"e1": agentEffect("e1")},
		windows: map[string]sourceledger.CommandWindow{"w": {ID: "w", ProjectID: "p", State: wire.SourceCommandWindowRunning}},
	}
	profile := New(ledger, fakeCause{})
	cases := map[string]struct {
		target wire.SourceStepBriefingTarget
		want   error
	}{
		"unknown kind":    {wire.SourceStepBriefingTarget{Kind: "other", TargetKey: "k"}, briefing.ErrInvalidTarget},
		"outside of one":  {wire.SourceStepBriefingTarget{Kind: "outside", TargetKey: "k", EffectIds: []string{"a"}}, briefing.ErrInvalidTarget},
		"changed scope":   {wire.SourceStepBriefingTarget{Kind: "effect", TargetKey: "e1", ScopeFingerprint: "stale"}, briefing.ErrTargetUnavailable},
		"running command": {wire.SourceStepBriefingTarget{Kind: "command", TargetKey: "command:w"}, briefing.ErrTargetUnavailable},
		"unknown effect":  {wire.SourceStepBriefingTarget{Kind: "effect", TargetKey: "missing"}, briefing.ErrTargetUnavailable},
	}
	for name, c := range cases {
		if _, err := profile.Resolve(t.Context(), target(c.target)); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The gutter names a change by its effect alone and lands on Walk's target.
func TestEffectNamedByIDMatchesTheWalkTarget(t *testing.T) {
	ledger := fakeLedger{effects: map[string]sourceledger.Effect{"e1": agentEffect("e1")}}
	profile := New(ledger, fakeCause{})
	fromGutter, err := profile.Resolve(t.Context(), target(wire.SourceStepBriefingTarget{Kind: "effect", TargetKey: "e1"}))
	testutil.FailErr(t, "resolve by id", err)
	fromWalk, err := profile.Resolve(t.Context(), target(wire.SourceStepBriefingTarget{Kind: "effect", TargetKey: "e1", ScopeFingerprint: fingerprint("e1", "v-e1"),
		Scope: wire.SourceStepBriefingScope{Kind: "file", RootIds: []string{""}, FileID: "f-e1"}}))
	testutil.FailErr(t, "resolve from walk", err)
	if fromGutter.TargetKey != fromWalk.TargetKey || fromGutter.ScopeFingerprint != fromWalk.ScopeFingerprint {
		t.Fatalf("gutter %s/%s, walk %s/%s", fromGutter.TargetKey, fromGutter.ScopeFingerprint, fromWalk.TargetKey, fromWalk.ScopeFingerprint)
	}
	for i := range fromGutter.Sections {
		a, _ := fromGutter.Sections[i].Manifest.Hash()
		b, _ := fromWalk.Sections[i].Manifest.Hash()
		if a != b {
			t.Fatalf("section %s hashes differ", fromGutter.Sections[i].ID)
		}
	}
}
