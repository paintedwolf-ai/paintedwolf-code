package oarcopy

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
	"slices"
	"testing"
)

func TestFactsFromDataPublishesReasonFieldAndPath(t *testing.T) {
	got := FactsFromData(map[string]any{
		"reason":     "unsupported field",
		"field":      "network",
		"path":       "src/main.go",
		"worker_leg": true,
	})
	if got["paintedwolf.rejection_reason"] != "unsupported field" {
		t.Fatalf("reason fact = %#v", got["paintedwolf.rejection_reason"])
	}
	if got["paintedwolf.rejection_field"] != "network" {
		t.Fatalf("field fact = %#v", got["paintedwolf.rejection_field"])
	}
	for _, key := range []string{"arg_validation_errors", "arg_validation_reason", "arg_validation_field"} {
		if _, present := got[key]; present {
			t.Fatalf("[OAR-PROF-3] diagnostic invented argument-check fact %s", key)
		}
	}
	if got["paintedwolf.path"] != "src/main.go" {
		t.Fatalf("path fact = %#v", got["paintedwolf.path"])
	}
	if _, ok := got["path"]; ok {
		t.Fatal("raw occurrence key leaked into published copy facts")
	}
	if got["paintedwolf.worker_leg"] != true {
		t.Fatalf("worker_leg = %#v", got["paintedwolf.worker_leg"])
	}
	if fields, _ := got["paintedwolf.capability_request_fields"].([]string); len(fields) != 0 || got["paintedwolf.capability_contract_known"] != false {
		t.Fatal("copy facts invented a capability contract for an unknown invocation")
	}
}

func TestFactsFromDataReasonFlags(t *testing.T) {
	got := FactsFromData(map[string]any{"reason": "bare_repo_scope"})
	if got["paintedwolf.bare_repo_scope"] != true {
		t.Fatalf("flag = %#v", got["paintedwolf.bare_repo_scope"])
	}
}

func TestParserReasonFlagsRemainExact(t *testing.T) {
	for _, reason := range []string{"unescaped_newline", "unexpected_end", "invalid_escape", "unknown", "unescaped_newline trailing"} {
		got := FactsFromData(map[string]any{"reason": reason})
		for _, candidate := range []string{"unescaped_newline", "unexpected_end", "invalid_escape"} {
			if (got["paintedwolf.json_"+candidate] == true) != (candidate == reason) {
				t.Fatalf("reason %q produced wrong parser flag %q: %#v", reason, candidate, got)
			}
		}
	}
}

func TestCapabilityFactsRequireReceivingToolIdentity(t *testing.T) {
	for _, tool := range []string{"", "unknown_tool", "http_request", "command"} {
		got := FactsFromData(map[string]any{"tool": tool, "capability_request_fields": []string{"invented"}, "supports_host_execution": true})
		contract, known := toolcontract.Lookup(tool)
		fields, _ := got["paintedwolf.capability_request_fields"].([]string)
		if !slices.Equal(fields, contract.CapabilityRequestFields()) || got["paintedwolf.capability_contract_known"] != known {
			t.Fatalf("diagnostic metadata replaced the %q contract: %#v", tool, got)
		}
	}
}

func TestExplicitArgumentObservationsRemainSeparateFromDiagnostics(t *testing.T) {
	got := FactsFromData(map[string]any{"reason": "connection ended", "field": "response", "arg_validation_reason": "invalid count", "arg_validation_field": "count"})
	if got["arg_validation_reason"] != "invalid count" || got["arg_validation_field"] != "count" || got["paintedwolf.rejection_reason"] != "connection ended" || got["paintedwolf.rejection_field"] != "response" {
		t.Fatalf("[OAR-PROF-3] observations and diagnostics were conflated: %#v", got)
	}
}

func TestPortableVocabularyAccessors(t *testing.T) {
	if !slices.Contains(RequiredCoreFacts(), "anchor") || !slices.Contains(RequiredCoreFunctions(), "fire_count_of") {
		t.Fatal("missing core facts or functions")
	}
	profiles := StandardProfileFacts()
	if !slices.Contains(profiles[ProfileTool], "tool") || !IsPortableFact("tool") || !IsPortableFact("anchor") || IsPortableFact("paintedwolf.path") {
		t.Fatalf("unexpected portable vocabulary: %#v", profiles)
	}
}

func TestCommandFormOfFollowsCatalog(t *testing.T) {
	for _, tc := range []struct {
		operation string
		want      string
	}{
		{"survey_repo", "repo-map"},
		{"replace_lines", "replace-lines"},
		{"code_rewrite", "code-rewrite"},
		{"edit", "edit"},
		{"write", ""},
		{"grep", ""},
		{"extract_archive", ""},
		{"command", ""},
		{"absent_tool", ""},
		{"", ""},
	} {
		if got := CommandFormOf(tc.operation); got != tc.want {
			t.Errorf("CommandFormOf(%q) = %q, want %q", tc.operation, got, tc.want)
		}
	}
}

func TestFactsFromDataDerivesCommandForm(t *testing.T) {
	got := FactsFromData(map[string]any{"native_operation": "survey_repo"})
	if got["paintedwolf.command_form"] != "repo-map" {
		t.Fatalf("command leg lost its catalog spelling: %#v", got["paintedwolf.command_form"])
	}
	multi := FactsFromData(map[string]any{"native_operation": "write"})
	if _, present := multi["paintedwolf.command_form"]; present {
		t.Fatal("multi-form operation invented a canonical command spelling")
	}
	plain := FactsFromData(map[string]any{"path": "src"})
	if _, present := plain["paintedwolf.command_form"]; present {
		t.Fatal("tool leg invented a command form")
	}
}

func TestMissingCapabilityFactsCoverNewestCapabilityFields(t *testing.T) {
	// capability_request.host_resources and capability_request.direct_ip are
	// the newest catalog capability fields. Their missing_capability_* facts
	// must stay published and computed like the older fields; the closed
	// isolation outcome inventory has no TRY codes for them yet, so their
	// policy-copy consumers land with the capability copy workstream.
	facts := map[string]any{}
	PublishInvocationRecovery(facts, "command", map[string]any{"command": "fixture"}, nil)
	if facts["paintedwolf.missing_capability_host_resources"] != true || facts["paintedwolf.missing_capability_direct_ip"] != true {
		t.Fatalf("unrequested capability fields must read missing: host_resources=%v direct_ip=%v",
			facts["paintedwolf.missing_capability_host_resources"], facts["paintedwolf.missing_capability_direct_ip"])
	}
	requested := map[string]any{"command": "fixture", "capability_request": map[string]any{"host_resources": []string{"camera"}, "direct_ip": true}}
	granted := map[string]any{}
	PublishInvocationRecovery(granted, "command", requested, nil)
	if granted["paintedwolf.missing_capability_host_resources"] != false || granted["paintedwolf.missing_capability_direct_ip"] != false {
		t.Fatalf("requested capability fields must not read missing: host_resources=%v direct_ip=%v",
			granted["paintedwolf.missing_capability_host_resources"], granted["paintedwolf.missing_capability_direct_ip"])
	}
}

func TestCommandRecoveryUsesInvocationIdentity(t *testing.T) {
	for _, tool := range []string{"command", "write", "edit", ""} {
		facts := FactsFromData(map[string]any{"tool": tool, "command_surface": true})
		if facts["paintedwolf.command_surface"] != (tool == "command") {
			t.Fatalf("wrong recovery surface for %q: %v", tool, facts)
		}
	}
}
