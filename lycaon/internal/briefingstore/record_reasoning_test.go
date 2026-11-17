package briefingstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/testutil"
)

const reasoningAudience = "session-a"

func reasoningRequest(excerpt string) briefing.Request {
	reasoning := briefing.ReasoningItem(briefing.ReasoningInput{Excerpt: &briefing.Reasoning{Text: excerpt, Ref: "message:out", Truncated: true}}, reasoningAudience)
	manifest := briefing.Manifest{ProfileID: "tool_activity", ProfileVersion: "1", SectionID: "purpose", Template: briefing.Template{ID: "action_purpose", Version: "2"},
		Contract: briefing.OneSentence, Items: []briefing.Item{
			{Key: "action", Role: briefing.RoleAction, SourceKind: "tool_call", Inclusion: briefing.IncludedWhole, Text: "tool=command", Audience: reasoningAudience},
			reasoning.Item,
		}}.Sealed()
	plan := briefing.SectionPlan{ID: "purpose", Mode: briefing.Generated, Manifest: manifest, Prompt: briefing.Prompt{System: "s", User: reasoning.Text, MaxTokens: 60}, Quality: reasoning.Quality}
	return briefing.Request{ProjectID: project, ProfileID: "tool_activity", Version: "1", TargetKey: "card", TargetKind: "activity",
		ScopeFingerprint: "scope-card", Policy: briefing.QueuedPolicy, Sections: []briefing.SectionPlan{plan}}
}

// The record shows the reasoning excerpt exactly as the prompt received it,
// and only to readers of its session; snapshots and results never carry it.
func TestRecordShowsReasoningAsSentToItsAudience(t *testing.T) {
	f := newFixture(t)
	engine, _, log := runEngine(t, f, "Pushes the release before tagging it.")
	excerpt := "Push first, then tag the release so the tag points at the pushed commit."
	req := reasoningRequest(excerpt)
	_, err := engine.Request(t.Context(), req)
	testutil.FailErr(t, "request", err)
	done := receive(t, log.seen)
	if done.Status != briefing.StatusComplete {
		t.Fatalf("settled %+v", done)
	}

	member := func(_ context.Context, audience string) (bool, error) {
		return audience == "" || audience == reasoningAudience, nil
	}
	record, err := f.store.Record(t.Context(), project, done.RecordID, member)
	testutil.FailErr(t, "record for a session reader", err)
	section := record.Sections[0]
	var shown string
	for _, item := range section.Items {
		if item.SourceKind == briefing.SourceReasoning {
			shown = item.Text
		}
	}
	if !section.Available || shown != excerpt || shown != req.Sections[0].Prompt.User {
		t.Fatalf("record showed %q; the prompt received %q", shown, req.Sections[0].Prompt.User)
	}
	if got := section.Result.EvidenceQuality; len(got) != 2 || got[0] != briefing.QualityReasoningIncluded || got[1] != briefing.QualityReasoningTruncated {
		t.Fatalf("record quality %v", got)
	}

	outsider := func(_ context.Context, audience string) (bool, error) { return audience == "", nil }
	hidden, err := f.store.Record(t.Context(), project, done.RecordID, outsider)
	testutil.FailErr(t, "record for an outside reader", err)
	if hidden.Sections[0].Available || len(hidden.Sections[0].Items) != 0 || hidden.Sections[0].Result != nil {
		t.Fatalf("an outside reader saw the section: %+v", hidden.Sections[0])
	}

	snapshot, err := f.store.Snapshot(t.Context(), SnapshotQuery{ProjectID: project, ProfileID: "tool_activity", Version: "1", Policy: briefing.QueuedPolicy, TargetKey: "card"})
	testutil.FailErr(t, "snapshot", err)
	encoded, err := json.Marshal(snapshot)
	testutil.FailErr(t, "encode snapshot", err)
	results, err := f.store.GetResults(t.Context(), project, []string{done.Sections[0].ManifestHash})
	testutil.FailErr(t, "results", err)
	if strings.Contains(string(encoded), excerpt) || strings.Contains(results[done.Sections[0].ManifestHash].Text, excerpt) {
		t.Fatal("reasoning text reached a snapshot or a stored result")
	}
}
