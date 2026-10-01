package workercontrol

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDecisionAttachmentComparisonKeepsBothIDs(t *testing.T) {
	const first = "11111111-1111-4111-8111-111111111111"
	const second = "22222222-2222-4222-8222-222222222222"
	for _, raw := range []any{[]any{first, second}, []string{first, second}} {
		got, err := parseDecisionAttachment(map[string]any{"artifact_ids": raw})
		testutil.FailErr(t, "parse intended comparison", err)
		if got.Single != "" || len(got.Compare) != 2 || got.Compare[0] != first || got.Compare[1] != second {
			t.Fatalf("comparison changed: %+v", got)
		}
	}
}
