package briefing

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func manifestFixture() Manifest {
	return Manifest{ProfileID: "tool_activity", ProfileVersion: "1", SectionID: "purpose", Template: Template{ID: "action_purpose", Version: "1"}, Contract: OneSentence,
		Items: []Item{
			{Key: "user_intent", Role: RoleGoal, SourceKind: "message", Ref: "message:1", Inclusion: IncludedWhole, Text: "Fix the build"},
			{Key: "tracked_plan", Role: RoleContext, SourceKind: "progress", Inclusion: Unavailable, Reason: "no immutable revision", Text: "ignored"},
		}}
}

// Golden vectors pin canonicalization: changing them changes every stored identity.
func TestManifestHashAndRecordIDAreStable(t *testing.T) {
	hash, err := manifestFixture().Hash()
	testutil.FailErr(t, "hash manifest", err)
	if want := "807eea58ed47f85af195fb10fbf4b000c9dd3996a9b0b64825ac2c43c41cf8f9"; hash != want {
		t.Fatalf("manifest hash %s; want %s", hash, want)
	}
	again, err := manifestFixture().Hash()
	testutil.FailErr(t, "hash manifest again", err)
	if again != hash {
		t.Fatal("manifest hash is not deterministic")
	}
	first := RecordID("tool_activity", "1", []SectionHash{{SectionID: "purpose", ManifestHash: hash}})
	if first != RecordID("tool_activity", "1", []SectionHash{{SectionID: "purpose", ManifestHash: hash}}) {
		t.Fatal("record id is not deterministic")
	}
	if first == RecordID("tool_activity", "2", []SectionHash{{SectionID: "purpose", ManifestHash: hash}}) {
		t.Fatal("record id ignores the profile version")
	}
}

func TestManifestHashTracksSubmittedMaterialOnly(t *testing.T) {
	base, err := manifestFixture().Hash()
	testutil.FailErr(t, "hash base", err)

	decomposed := manifestFixture()
	decomposed.Items[0].Text = "Fix the café"
	composed := manifestFixture()
	composed.Items[0].Text = "Fix the café"
	a, _ := decomposed.Hash()
	b, _ := composed.Hash()
	if a != b {
		t.Fatal("NFC-equivalent text hashed differently")
	}

	unsent := manifestFixture()
	unsent.Items[1].Text = "different text that was never submitted"
	if h, _ := unsent.Hash(); h != base {
		t.Fatal("text of an unavailable item changed the hash")
	}

	changed := manifestFixture()
	changed.Items[1].Inclusion = IncludedWhole
	if h, _ := changed.Hash(); h == base {
		t.Fatal("inclusion state did not change the hash")
	}

	contract := manifestFixture()
	contract.Contract = TwoSentences
	if h, _ := contract.Hash(); h == base {
		t.Fatal("output contract did not change the hash")
	}

	incomplete := manifestFixture()
	incomplete.Items[0].Role = ""
	if _, err := incomplete.Hash(); err == nil {
		t.Fatal("incomplete item hashed")
	}
}
