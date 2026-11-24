package progress

import "testing"

func TestSynthesisReconcileOnly(t *testing.T) {
	current := NewSnapshot("run",
		Row{ID: "p1", State: ProgressStatePending, Label: "survey"},
		Row{ID: "p2", State: ProgressStateDone, Label: "scan"},
	)
	done := ProgressStateDone
	na := ProgressStateNA
	pending := ProgressStatePending
	relabeled := "expanded survey"

	if !(Mutation{Update: []UpdateRowSpec{{ID: "p1", State: &done}}}).IsSynthesisReconcileOnly(current) {
		t.Fatal("closing pending row to done must be allowed")
	}
	if !(Mutation{Update: []UpdateRowSpec{{ID: "p1", State: &na}}, AcknowledgeResults: []string{"scout#1"}}).IsSynthesisReconcileOnly(current) {
		t.Fatal("marking row na and acknowledging results must be allowed")
	}
	if !(Mutation{AcknowledgeResults: []string{"scout#1"}}).IsSynthesisReconcileOnly(current) {
		t.Fatal("pure handle acknowledgement must be allowed")
	}
	if (Mutation{Add: []AddRowSpec{{Label: "new work", State: ProgressStatePending}}}).IsSynthesisReconcileOnly(current) {
		t.Fatal("adding rows on synthesis must be rejected")
	}
	if (Mutation{Replace: []RowSpec{{Label: "survey", State: ProgressStateDone}}}).IsSynthesisReconcileOnly(current) {
		t.Fatal("replace on synthesis must be rejected")
	}
	if (Mutation{Remove: []string{"p1"}}).IsSynthesisReconcileOnly(current) {
		t.Fatal("remove on synthesis must be rejected")
	}
	if (Mutation{Update: []UpdateRowSpec{{ID: "p2", State: &pending}}}).IsSynthesisReconcileOnly(current) {
		t.Fatal("reopening row on synthesis must be rejected")
	}
	if (Mutation{Update: []UpdateRowSpec{{ID: "p1", State: &done, Label: &relabeled}}}).IsSynthesisReconcileOnly(current) {
		t.Fatal("relabeling row on synthesis must be rejected")
	}
	if (Mutation{Update: []UpdateRowSpec{{ID: "p99", State: &done}}}).IsSynthesisReconcileOnly(current) {
		t.Fatal("unknown row ID on synthesis must be rejected")
	}
	if (Mutation{Update: []UpdateRowSpec{{ID: "p1", State: &done}}}).IsSynthesisReconcileOnly(NewSnapshot("run")) {
		t.Fatal("missing plan cannot reconcile on synthesis")
	}
}
