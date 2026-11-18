package contract

import (
	"context"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestAltitudeInvariantCuratorSnapshotIsolation(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	snapshot := snapshotFromReadLine(t, "current.go", "5| current only", 5)
	provider := &stubCuratorProvider{
		id: "lite",
		responses: []string{
			`{"selections":[{"path":"prior.go","line":1,"excerpt":"prior survey"}],"gloss":[]}`,
		},
	}
	cur := newCuratorFixture(t, provider)
	got, err := cur.Curate(context.Background(), snapshot, llm.CurationFocus{Target: "focus"}, 3)
	contractcheck.FailErr(t, "Curate", err)
	if len(got.Selections) != 0 {
		t.Fatalf("selection outside snapshot must be dropped: %#v", got.Selections)
	}
}

func TestAltitudeInvariantNoCommissionMatchedOnly(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	snapshot := snapshotFromReadLine(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &stubCuratorProvider{
		id: "lite",
		responses: []string{
			`{"selections":[{"path":"pkg/a.go","line":10,"excerpt":"func entry"}],"gloss":[{"label":"entry"}]}`,
		},
	}
	cur := newCuratorFixture(t, provider)
	got, err := cur.Curate(context.Background(), snapshot, llm.CurationFocus{Target: "entry"}, 3)
	contractcheck.FailErr(t, "Curate", err)
	for _, sel := range got.Selections {
		if sel.Resolution.Verdict != evidence.VerdictMatched {
			t.Fatalf("curated selection must be matched verbatim: %#v", sel)
		}
	}
}

func sortedSchemaPropertyKeys(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	var keys []string
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
