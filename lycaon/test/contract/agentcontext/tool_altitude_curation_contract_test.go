package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestAltitudeInvariantNoCommissionMatchedOnly(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	snapshot := snapshotFromReadLine(t, "pkg/a.go", "10| func entry() {}", 10)
	provider := &stubCuratorProvider{
		id: "lite",
		responses: []string{
			`{"selections":[{"path":"pkg/a.go","line":10,"excerpt":"func entry"}],"gloss":[{"label":"entry"}]}`,
		},
	}
	cur, err := llm.NewTestRegistrySummarizer(provider)
	contractcheck.FailErr(t, "NewTestRegistrySummarizer", err)
	got, err := cur.Curate(context.Background(), snapshot, llm.CurationFocus{Target: "entry"}, 3)
	contractcheck.FailErr(t, "Curate", err)
	if len(got.Selections) != 1 || got.Selections[0].Resolution.Verdict != evidence.VerdictMatched {
		t.Fatalf("curated selections = %#v, want the one verbatim match", got.Selections)
	}
}
