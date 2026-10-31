package authzcontext

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestVerifyRejectsUnsupportedStoredHashVersions(t *testing.T) {
	contextRow := Context{SessionID: "session", ContextSeq: 1, HashVersion: HashVersion1}
	var err error
	contextRow.RowHash, err = ComputeRowHash(HashVersion1, "session", 1, "", chainPayloadFromContext(contextRow), "")
	testutil.FailErr(t, "hash context", err)
	eventRow := Event{SessionID: "session", EventSeq: 1, HashVersion: HashVersion1}
	eventRow.RowHash, err = ComputeEventRowHash(HashVersion1, "session", 1, "", eventChainPayloadFromEvent(eventRow), "")
	testutil.FailErr(t, "hash event", err)
	if broken := VerifyContexts("session", []Context{contextRow}); broken != nil {
		t.Fatalf("valid context rejected: %+v", broken)
	}
	if broken := VerifyEvents("session", []Event{eventRow}); broken != nil {
		t.Fatalf("valid event rejected: %+v", broken)
	}
	for _, version := range []int{0, -1, 2} {
		contextRow.HashVersion = version
		eventRow.HashVersion = version
		if broken := VerifyContexts("session", []Context{contextRow}); broken == nil || broken.Reason != "unsupported hash_version" {
			t.Fatalf("context version %d: %+v", version, broken)
		}
		if broken := VerifyEvents("session", []Event{eventRow}); broken == nil || broken.Reason != "unsupported hash_version" {
			t.Fatalf("event version %d: %+v", version, broken)
		}
	}
}
