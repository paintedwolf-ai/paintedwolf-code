package kick

import (
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPolicyQueueRetainsSubjectsAndFrozenLeases(t *testing.T) {
	k := &KickEngine{}
	entry := guidance.PolicyFeedback{Anchor: "tool.pre_invoke", Rule: "example/CHECK", Effect: "nudge", Copy: map[string]string{"what": "frozen"}, ToolFeedback: api.ToolFeedback{Code: "CHECK", Details: map[string]any{"nested": map[string]any{"value": "original"}}, Subject: &api.FeedbackSubject{Kind: "task", ID: "first"}}}
	k.QueuePolicyFeedback("session", []guidance.PolicyFeedback{entry, entry})
	entry.Details["nested"].(map[string]any)["value"] = "mutated"
	lease := k.LeasePolicyFeedback("session")
	if len(lease.Entries) != 2 || lease.Entries[0].Details["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("queue did not freeze occurrence: %#v", lease)
	}
	for n := range 100 {
		entry.Subject.ID = fmt.Sprint(n)
		k.QueuePolicyFeedback("session", []guidance.PolicyFeedback{entry})
	}
	lease.Entries[0].Copy["what"] = "changed"
	retry := k.LeasePolicyFeedback("session")
	if retry.ID != lease.ID || retry.Sequence != lease.Sequence || len(retry.Entries) != 2 || retry.Entries[0].Copy["what"] != "frozen" {
		t.Fatalf("staged lease changed: %#v", retry)
	}
	k.AckPolicyFeedback("session", lease.Sequence+1)
	if k.LeasePolicyFeedback("session").ID != lease.ID {
		t.Fatal("wrong acknowledgement consumed lease")
	}
	k.AckPolicyFeedback("session", lease.Sequence)
	next := k.LeasePolicyFeedback("session")
	if len(next.Entries) != 100 || next.ID == lease.ID {
		t.Fatalf("distinct subjects lost: %d", len(next.Entries))
	}
	k.ClearPending("session")
	if len(k.LeasePolicyFeedback("session").Entries) != 0 {
		t.Fatal("clear retained policy feedback")
	}
}
