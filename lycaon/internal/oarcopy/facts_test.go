package oarcopy

import "testing"

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
	if _, present := got["paintedwolf.capability_request_fields"]; present {
		t.Fatal("copy facts invented capabilities absent from the invocation")
	}
}

func TestFactsFromDataReasonFlags(t *testing.T) {
	got := FactsFromData(map[string]any{"reason": "bare_repo_scope"})
	if got["paintedwolf.bare_repo_scope"] != true {
		t.Fatalf("flag = %#v", got["paintedwolf.bare_repo_scope"])
	}
}

func TestFactsFromDataPreservesMeasuredCapabilityFields(t *testing.T) {
	got := FactsFromData(map[string]any{"capability_request_fields": []string{"loopback_connect.ports"}})
	fields, ok := got["paintedwolf.capability_request_fields"].([]string)
	if !ok || len(fields) != 1 || fields[0] != "loopback_connect.ports" {
		t.Fatalf("invocation fields changed: %#v", got)
	}
}

func TestExplicitArgumentObservationsRemainSeparateFromDiagnostics(t *testing.T) {
	got := FactsFromData(map[string]any{"reason": "connection ended", "field": "response", "arg_validation_reason": "invalid count", "arg_validation_field": "count"})
	if got["arg_validation_reason"] != "invalid count" || got["arg_validation_field"] != "count" || got["paintedwolf.rejection_reason"] != "connection ended" || got["paintedwolf.rejection_field"] != "response" {
		t.Fatalf("[OAR-PROF-3] observations and diagnostics were conflated: %#v", got)
	}
}
