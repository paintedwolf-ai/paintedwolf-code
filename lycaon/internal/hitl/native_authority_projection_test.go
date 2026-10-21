package hitl_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestNativeAuthorityProjectionPreservesExactRawPaths(t *testing.T) {
	for _, path := range []string{"/fixture/socket\xff ", "/fixture/socket\xfe ", "/fixture/socket\ufffd "} {
		memory := &nativeReviewMemory{}
		once := hitl.CurrentActionOption()
		once.Authority = []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthoritySocketPermit, SessionID: "session", ToolCallID: "model-call", ExecutionID: "actual-child", ActionDigest: "actual-reviewed-action", Sockets: []hitl.ApprovalSocketTarget{{ApprovedPath: path, ResolvedPath: path}}}}
		original := []hitl.ApprovalOption{once}
		projection, release, err := hitl.NewNativeAuthorityProjection(t.Context(), memory, original)
		if err != nil {
			t.Fatal(err)
		}
		if !projection.Valid() || projection.Representation() != "native_approval_authority_v1" || !strings.Contains(projection.Canonical(), "native_approval_authority") {
			t.Fatal("authority lost its explicit domain")
		}
		if _, wrongRelease, err := toolcontract.RestoreReviewedOperationInputs(t.Context(), memory, projection.Representation(), projection.Digest(), projection.Canonical()); err == nil || wrongRelease != nil {
			t.Fatal("native authority became executable operation inputs")
		}
		restored, releaseRestored, err := hitl.RestoreNativeAuthorityProjection(t.Context(), memory, projection.Representation(), projection.Digest(), projection.Canonical())
		if err != nil {
			t.Fatal(err)
		}
		options, releaseOptions, err := restored.Options(t.Context(), memory)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(options, original) || options[0].Authority[0].Sockets[0].ApprovedPath != path {
			t.Fatal("native authority path bytes/whitespace or typed metadata changed")
		}
		releaseOptions()
		releaseRestored()
		release()
		if memory.held != 0 {
			t.Fatal("native authority leases leaked")
		}
	}
}

func TestNativeAuthorityProjectionRejectsUnknownFieldsEnumsAndUnadmittedWork(t *testing.T) {
	memory := &nativeReviewMemory{}
	projection, release, err := hitl.NewNativeAuthorityProjection(t.Context(), memory, []hitl.ApprovalOption{hitl.CurrentActionOption()})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, change := range []string{"unknown field", "missing field", "unknown enum"} {
		value, releaseValue, err := toolcontract.RestoreNativeValue(t.Context(), memory, toolcontract.ApprovalAuthorityDomain, projection.Representation(), projection.Digest(), projection.Canonical())
		if err != nil {
			t.Fatal(err)
		}
		tree, releaseTree, err := value.Value(t.Context(), memory)
		if err != nil {
			t.Fatal(err)
		}
		option := tree.([]any)[0].(map[string]any)
		switch change {
		case "unknown field":
			option["unrecognized_native_field"] = true
		case "missing field":
			delete(option, "kind")
		case "unknown enum":
			option["kind"] = "invented_option_kind"
		}
		tampered, releaseTampered, err := toolcontract.NewNativeValue(t.Context(), memory, toolcontract.ApprovalAuthorityDomain, tree)
		if err != nil {
			t.Fatal(err)
		}
		if _, retained, err := hitl.RestoreNativeAuthorityProjection(t.Context(), memory, tampered.Representation(), tampered.Digest(), tampered.Canonical()); err == nil || retained != nil {
			t.Fatal("unknown native authority shape acquired meaning", change)
		}
		releaseTampered()
		releaseTree()
		releaseValue()
	}
	memory.refuse = true
	if _, retained, err := hitl.NewNativeAuthorityProjection(t.Context(), memory, []hitl.ApprovalOption{hitl.CurrentActionOption()}); err == nil || retained != nil {
		t.Fatal("native typed conversion allocated without admission")
	}
	memory.refuse = false
	release()
	if memory.held != 0 {
		t.Fatal("refused native authority binding retained state")
	}
}
