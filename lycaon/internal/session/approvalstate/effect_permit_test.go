package approvalstate

import (
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"testing"
)

func TestNativeChildOncePermitNeverCoversSiblingOrStructuredCall(t *testing.T) {
	first := hitl.EffectPermitIdentity{SessionID: "session", ToolCallID: "actual-model-call", ExecutionID: "first-child"}
	sibling := first
	sibling.ExecutionID = "second-child"
	legacy := first
	legacy.ExecutionID = ""
	sockets := NewSocketCapabilityRuntime()
	grant := confine.SocketGrant{ApprovedPath: "/fixture/service.sock", ResolvedPath: "/fixture/service.sock"}
	sockets.IssuePermit(first, "same-semantic-action", grant)
	direct := NewDirectIPCapabilityRuntime()
	direct.IssuePermit(first, "same-semantic-action", "request", "confinement")
	for _, other := range []hitl.EffectPermitIdentity{sibling, legacy} {
		if ok, _ := sockets.ConsumePermit(other, "same-semantic-action", grant); ok {
			t.Fatal("socket once permit crossed child identity")
		}
		if direct.Authorized(other, "same-semantic-action") {
			t.Fatal("direct once permit crossed child identity")
		}
	}
	if ok, err := sockets.ConsumePermit(first, "same-semantic-action", grant); err != nil || !ok {
		t.Fatalf("actual socket child lost permit: %v", err)
	}
	if ok, err := direct.ConsumePermit(first, "same-semantic-action", "request", "confinement"); err != nil || !ok {
		t.Fatalf("actual direct child lost permit: %v", err)
	}
	sockets.IssuePermit(legacy, "same-semantic-action", grant)
	direct.IssuePermit(legacy, "same-semantic-action", "request", "confinement")
	if ok, _ := sockets.ConsumePermit(first, "same-semantic-action", grant); ok || direct.Authorized(first, "same-semantic-action") {
		t.Fatal("retained structured authority granted native child")
	}
	if ok, _ := sockets.ConsumePermit(legacy, "same-semantic-action", grant); !ok || !direct.Authorized(legacy, "same-semantic-action") {
		t.Fatal("native correlation broke retained structured authority")
	}
}
