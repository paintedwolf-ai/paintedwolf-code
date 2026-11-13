package briefing

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDisplayCarriesSubmittedExcerptsOnlyForSentItems(t *testing.T) {
	manifest := Manifest{ProfileID: "p", ProfileVersion: "1", SectionID: "s", Template: Template{ID: "t", Version: "1"}, Contract: OneSentence, Items: []Item{
		{Key: "sent", Role: RoleGoal, SourceKind: "message", Inclusion: IncludedWhole, Text: "excerpt one"},
		{Key: "gap", Role: RoleContext, SourceKind: "plan", Inclusion: Unavailable, Reason: "no revision", Text: "never sent"},
	}}
	raw, err := manifest.Display()
	testutil.FailErr(t, "display", err)
	var decoded struct {
		SectionID string `json:"section_id"`
		Items     []struct {
			Key, Text, Digest string
		} `json:"items"`
	}
	testutil.FailErr(t, "decode", json.Unmarshal(raw, &decoded))
	if decoded.SectionID != "s" || len(decoded.Items) != 2 {
		t.Fatalf("display lost structure: %s", raw)
	}
	if decoded.Items[0].Text != "excerpt one" || decoded.Items[0].Digest == "" || decoded.Items[1].Text != "" {
		t.Fatalf("display excerpts wrong: %+v", decoded.Items)
	}
	if manifest.Unavailable() != 1 || manifest.Coverage()[1].Reason != "no revision" {
		t.Fatalf("coverage %+v", manifest.Coverage())
	}
}
