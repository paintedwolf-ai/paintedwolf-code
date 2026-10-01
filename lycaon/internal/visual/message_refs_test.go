package visual

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// Every claim a transcript row makes is derived in one function, so a referrer
// this misses is an artifact nothing pins.
func TestMessageRefsCoversEveryTranscriptReferrer(t *testing.T) {
	msg := api.Message{
		ID:          "msg-1",
		Role:        api.MessageRoleAssistant,
		ArtifactIDs: []string{"art-present", " art-present-2 "},
		ToolResult: &api.ToolResult{
			ToolCallID: "call-1",
			Visual:     &api.VisualArtifact{ID: "art-tool"},
		},
		WorkflowFeedback: &api.WorkflowFeedbackMeta{
			ArtifactID:  "art-ask",
			ArtifactIDs: []string{"art-compare-a", "art-compare-b"},
		},
	}
	refs := MessageRefs("proj-1", "sess-1", msg)

	byArtifact := map[string]api.ArtifactReferenceKind{}
	for _, ref := range refs {
		if ref.ProjectID != "proj-1" || ref.MessageID != "msg-1" || ref.SessionID != "sess-1" {
			t.Fatalf("ref lost its referrer: %+v", ref)
		}
		byArtifact[ref.ArtifactID] = ref.Kind
	}
	want := map[string]api.ArtifactReferenceKind{
		"art-present":   api.ArtifactReferenceKindMessagePresent,
		"art-present-2": api.ArtifactReferenceKindMessagePresent,
		"art-tool":      api.ArtifactReferenceKindToolResult,
		"art-ask":       api.ArtifactReferenceKindMessageAttachment,
		"art-compare-a": api.ArtifactReferenceKindMessageAttachment,
		"art-compare-b": api.ArtifactReferenceKindMessageAttachment,
	}
	if len(byArtifact) != len(want) {
		t.Fatalf("claims = %+v want %d referrers", byArtifact, len(want))
	}
	for id, kind := range want {
		if byArtifact[id] != kind {
			t.Fatalf("%s claimed as %q want %q", id, byArtifact[id], kind)
		}
	}
	for _, ref := range refs {
		if ref.ArtifactID == "art-tool" && ref.ToolCallID != "call-1" {
			t.Fatalf("tool_result claim lost its call id: %+v", ref)
		}
	}
}

// A human attaching an image and a coordinator presenting one are both claims,
// but only one of them is the model showing its work.
func TestMessageRefsDistinguishesAttachmentFromPresent(t *testing.T) {
	user := MessageRefs("proj-1", "sess-1", api.Message{
		ID: "msg-user", Role: api.MessageRoleUser, ArtifactIDs: []string{"art-1"},
	})
	if len(user) != 1 || user[0].Kind != api.ArtifactReferenceKindMessageAttachment {
		t.Fatalf("user message claims = %+v want message_attachment", user)
	}
	assistant := MessageRefs("proj-1", "sess-1", api.Message{
		ID: "msg-model", Role: api.MessageRoleAssistant, ArtifactIDs: []string{"art-1"},
	})
	if len(assistant) != 1 || assistant[0].Kind != api.ArtifactReferenceKindMessagePresent {
		t.Fatalf("assistant message claims = %+v want message_present", assistant)
	}
}

func TestRefRowIDIsStableAndDistinguishing(t *testing.T) {
	a := RefRowID(api.ArtifactReferenceKindMessagePresent, "art-1", "msg-1", "sess-1", "")
	if a != RefRowID(api.ArtifactReferenceKindMessagePresent, "art-1", "msg-1", "sess-1", "") {
		t.Fatal("the same claim produced two row ids")
	}
	for _, other := range []string{
		RefRowID(api.ArtifactReferenceKindToolResult, "art-1", "msg-1", "sess-1", ""),
		RefRowID(api.ArtifactReferenceKindMessagePresent, "art-2", "msg-1", "sess-1", ""),
		RefRowID(api.ArtifactReferenceKindMessagePresent, "art-1", "msg-2", "sess-1", ""),
		RefRowID(api.ArtifactReferenceKindMessagePresent, "art-1", "msg-1", "sess-2", ""),
		RefRowID(api.ArtifactReferenceKindMessagePresent, "art-1", "msg-1", "sess-1", "call-1"),
	} {
		if other == a {
			t.Fatal("two different claims collide on one row id")
		}
	}
}

// Nothing in this list is a claim the host can honour without an artifact.
func TestMessageRefsIgnoresIncompleteReferrers(t *testing.T) {
	if refs := MessageRefs("", "sess-1", api.Message{ID: "msg-1", ArtifactIDs: []string{"art-1"}}); refs != nil {
		t.Fatalf("claims without a project = %+v want none", refs)
	}
	if refs := MessageRefs("proj-1", "sess-1", api.Message{ArtifactIDs: []string{"art-1"}}); refs != nil {
		t.Fatalf("claims without a message = %+v want none", refs)
	}
	refs := MessageRefs("proj-1", "sess-1", api.Message{ID: "msg-1", ArtifactIDs: []string{"", "   "}})
	if len(refs) != 0 {
		t.Fatalf("blank artifact ids became claims: %+v", refs)
	}
}
