package contract

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Curated gloss stays bounded and never carries verbatim code claims.
func TestAltitudeGlossIsFenced(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	snapshot := snapshotFromReadLine(t, "a.go", "1| x", 1)
	var gloss []string
	for i := 0; i < 12; i++ {
		gloss = append(gloss, fmt.Sprintf(`{"label":"hint-%d"}`, i))
	}
	provider := &stubCuratorProvider{
		id: "lite",
		responses: []string{fmt.Sprintf(
			`{"selections":[{"path":"a.go","line":1,"excerpt":"x"}],"gloss":[%s]}`,
			strings.Join(gloss, ","),
		)},
	}
	cur, err := llm.NewTestRegistrySummarizer(provider)
	contractcheck.FailErr(t, "NewTestRegistrySummarizer", err)
	got, err := cur.Curate(context.Background(), snapshot, llm.CurationFocus{Target: "focus"}, 3)
	contractcheck.FailErr(t, "Curate", err)
	if len(got.Gloss) > 5 {
		t.Fatalf("gloss lines = %d want <=5", len(got.Gloss))
	}
	for _, g := range got.Gloss {
		if strings.Contains(g.Label, "func ") {
			t.Fatalf("gloss must not carry verbatim code claims: %q", g.Label)
		}
	}
}
