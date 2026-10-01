package progress

import "testing"

func TestSynthesisReconcileOnly_allowsClosePending(t *testing.T) {
	current := "## Progress\n- [ ] survey\n- [x] scan"
	proposed := "## Progress\n- [x] survey\n- [x] scan"
	if !SynthesisReconcileOnly(current, proposed) {
		t.Fatal("closing a pending row must be allowed on synthesis")
	}
}

func TestSynthesisReconcileOnly_rejectsNewPending(t *testing.T) {
	current := "## Progress\n- [x] survey"
	proposed := "## Progress\n- [x] survey\n- [ ] new work"
	if SynthesisReconcileOnly(current, proposed) {
		t.Fatal("new pending row must be rejected on synthesis")
	}
}

func TestSynthesisReconcileOnly_rejectsMissingPlan(t *testing.T) {
	if SynthesisReconcileOnly("", "## Progress\n- [x] survey") {
		t.Fatal("cannot reconcile from missing plan")
	}
}

func TestSynthesisReconcileOnly_rejectsReopen(t *testing.T) {
	current := "## Progress\n- [x] survey"
	proposed := "## Progress\n- [ ] survey"
	if SynthesisReconcileOnly(current, proposed) {
		t.Fatal("reopening a done row must be rejected")
	}
}

func TestSynthesisReconcileOnly_allowsNA(t *testing.T) {
	current := "## Progress\n- [ ] survey\n- [ ] review"
	proposed := "## Progress\n- [~] survey\n- [x] review"
	if !SynthesisReconcileOnly(current, proposed) {
		t.Fatal("marking rows done or n/a must be allowed")
	}
}
