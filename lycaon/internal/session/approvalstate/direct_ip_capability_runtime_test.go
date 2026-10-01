package approvalstate_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDirectIPCapabilityRuntimePermitOnce(t *testing.T) {
	rt := approvalstate.NewDirectIPCapabilityRuntime()
	rt.IssuePermit("sess-1", "tc-1", "action-1", "req-1", "conf-1")
	if !rt.Authorized("sess-1", "tc-1", "action-1") {
		t.Fatal("Authorized before consume")
	}
	ok, err := rt.ConsumePermit("sess-1", "tc-1", "action-1", "req-1", "conf-1")
	testutil.FailErr(t, "ConsumePermit", err)
	if !ok {
		t.Fatal("first consume must succeed")
	}
	if rt.Authorized("sess-1", "tc-1", "action-1") {
		t.Fatal("Authorized after consume must be false")
	}
	ok, err = rt.ConsumePermit("sess-1", "tc-1", "action-1", "req-1", "conf-1")
	if ok || err != nil {
		t.Fatalf("second consume must fail: ok=%v err=%v", ok, err)
	}
}

func TestDirectIPCapabilityRuntimeMismatch(t *testing.T) {
	rt := approvalstate.NewDirectIPCapabilityRuntime()
	rt.IssuePermit("sess-1", "tc-1", "action-1", "req-1", "conf-1")
	ok, err := rt.ConsumePermit("sess-1", "tc-1", "action-1", "req-OTHER", "conf-1")
	if ok || err == nil {
		t.Fatalf("request digest mismatch must fail: ok=%v err=%v", ok, err)
	}
	rt.IssuePermit("sess-1", "tc-1", "action-1", "req-1", "conf-1")
	ok, err = rt.ConsumePermit("sess-1", "tc-1", "action-1", "req-1", "conf-OTHER")
	if ok || err == nil {
		t.Fatalf("confine digest mismatch must fail: ok=%v err=%v", ok, err)
	}
}

func TestDirectIPCapabilityRuntimeForgetSession(t *testing.T) {
	rt := approvalstate.NewDirectIPCapabilityRuntime()
	rt.IssuePermit("sess-1", "tc-1", "action-1", "req-1", "conf-1")
	rt.ForgetSession("sess-1")
	if rt.Authorized("sess-1", "tc-1", "action-1") {
		t.Fatal("ForgetSession must drop permits")
	}
}
