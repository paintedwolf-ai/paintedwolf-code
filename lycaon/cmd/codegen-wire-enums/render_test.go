package main

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConstNaming(t *testing.T) {
	e := EnumDef{Name: "EventTopic", GoConstPrefix: "EventTopic", Values: []EnumValue{
		{ID: "llm", Go: "LLM"},
		{ID: "model_policy"},
	}}
	n, err := e.constName(e.Values[0])
	if err != nil || n != "EventTopicLLM" {
		t.Fatalf("llm → %q %v", n, err)
	}
	n, err = e.constName(e.Values[1])
	if err != nil || n != "EventTopicModelPolicy" {
		t.Fatalf("model_policy → %q %v", n, err)
	}

	bucket := EnumDef{Name: "LocalDataBucketId", GoConstPrefix: "LocalDataBucket", Values: []EnumValue{
		{ID: "osv_cache", Go: "OSVCache"},
		{ID: "web_index"},
	}}
	n, err = bucket.constName(bucket.Values[0])
	if err != nil || n != "LocalDataBucketOSVCache" {
		t.Fatalf("osv_cache → %q %v", n, err)
	}
	n, err = bucket.constName(bucket.Values[1])
	if err != nil || n != "LocalDataBucketWebIndex" {
		t.Fatalf("web_index → %q %v", n, err)
	}
}

func TestRenderGoEmitsAllValues(t *testing.T) {
	e := EnumDef{Name: "FindingLevel", GoConstPrefix: "FindingLevel", Values: []EnumValue{
		{ID: "critical"},
		{ID: "high"},
	}}
	body, err := renderGo(e)
	testutil.FailErr(t, "renderGo failed", err)
	s := string(body)
	for _, want := range []string{
		"var allFindingLevelValues = []FindingLevel",
		"func AllFindingLevelValues() []FindingLevel",
		"return append([]FindingLevel(nil), allFindingLevelValues...)",
		"FindingLevelCritical",
		"FindingLevelHigh",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRenderGoEmitsDeprecatedSuccessors(t *testing.T) {
	e := EnumDef{Name: "FindingKind", GoConstPrefix: "FindingKind", Values: []EnumValue{
		{ID: "old_kind", Deprecated: true, Successor: "new_kind"},
		{ID: "new_kind"},
	}}
	body, err := renderGo(e)
	testutil.FailErr(t, "renderGo failed", err)
	s := string(body)
	for _, want := range []string{
		"func DeprecatedFindingKindSuccessors() map[FindingKind]FindingKind",
		"FindingKindOldKind: FindingKindNewKind",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestRenderGoEmitsHTTPStatus(t *testing.T) {
	e := EnumDef{Name: "ApiErrorCode", GoConstPrefix: "ApiErrorCode", HTTPStatus: true, Values: []EnumValue{
		{ID: "scan_not_found", Status: 404},
		{ID: "session_not_found", Status: 404},
		{ID: "invalid_request", Status: 400},
	}}
	body, err := renderGo(e)
	testutil.FailErr(t, "renderGo failed", err)
	s := string(body)
	for _, want := range []string{
		"func (c ApiErrorCode) HTTPStatus() int",
		"case ApiErrorCodeInvalidRequest:\n\t\treturn 400",
		"case ApiErrorCodeScanNotFound,\n\t\tApiErrorCodeSessionNotFound:\n\t\treturn 404",
		"default:\n\t\treturn 500",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
}

func TestValidateStatusRequiresDeclaration(t *testing.T) {
	cases := []struct {
		name string
		def  EnumDef
		want string
	}{
		{"status without http_status", EnumDef{Name: "FindingKind", Values: []EnumValue{{ID: "a", Status: 404}}}, "only valid for an enum that declares http_status"},
		{"http_status without status", EnumDef{Name: "ApiErrorCode", HTTPStatus: true, Values: []EnumValue{{ID: "a"}}}, "needs an error status"},
		{"success status", EnumDef{Name: "ApiErrorCode", HTTPStatus: true, Values: []EnumValue{{ID: "a", Status: 200}}}, "needs an error status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.def.validate(tc.def.Name + ".yaml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate() = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestRenderTSStateMachineProjectsForApplyingClients(t *testing.T) {
	e := EnumDef{
		Name: "SessionStatus",
		Values: []EnumValue{
			{ID: "preparing"}, {ID: "idle"}, {ID: "busy"}, {ID: "error"},
		},
		StateMachine: &StateMachine{
			Initial: []string{"preparing", "idle"},
			Transitions: map[string][]string{
				"preparing": {"idle", "error"},
				"idle":      {"busy", "error"},
				"busy":      {"idle", "error"},
				"error":     {"idle", "busy"},
			},
		},
	}
	out := string(renderTSStateMachine(e))

	if !strings.Contains(out, "Object.values(SESSION_STATUS_TRANSITIONS).flat()") {
		t.Fatalf("enterable set is not derived from the transition table:\n%s", out)
	}

	// Clients may skip intermediate states already validated by the store.
	for _, absent := range []string{"isInitialSessionStatus", "canTransitionSessionStatus"} {
		if strings.Contains(out, absent) {
			t.Fatalf("client projection should not carry %s", absent)
		}
	}
	if !strings.Contains(out, "export function isEnterableSessionStatus") {
		t.Fatalf("client projection missing isEnterableSessionStatus:\n%s", out)
	}
	if strings.Count(out, "export ") != 1 {
		t.Fatalf("client projection should export exactly one symbol:\n%s", out)
	}
}
