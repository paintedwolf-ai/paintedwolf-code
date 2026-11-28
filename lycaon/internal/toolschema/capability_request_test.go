package toolschema_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func capabilityRequest(t *testing.T, entry toolschema.Entry) map[string]any {
	t.Helper()
	properties, _ := entry.Schema["properties"].(map[string]any)
	request, ok := properties["capability_request"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no capability_request: %v", entry.Schema)
	}
	return request
}

func requestFields(request map[string]any) []string {
	fields, _ := request["properties"].(map[string]any)
	out := make([]string, 0, len(fields))
	for field := range fields {
		out = append(out, field)
	}
	slices.Sort(out)
	return out
}

func TestDeclaredCapabilityRequestReceivesTheContractFields(t *testing.T) {
	entry, err := toolschema.ParseEntry("command", []byte("description: run\nschema:\n  type: object\n  properties:\n    capability_request: {description: Extra access.}\n"))
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	request := capabilityRequest(t, entry)
	contract, _ := toolcontract.Lookup("command")
	want := contract.CapabilityRequestFields()
	slices.Sort(want)
	if got := requestFields(request); !slices.Equal(got, want) {
		t.Fatalf("command capability_request fields = %v, want %v", got, want)
	}
	if request["additionalProperties"] != false || request["type"] != "object" || request["description"] != "Extra access." {
		t.Fatalf("capability_request object = %v", request)
	}
}

func TestDeclaredFieldKeysMergeOverTheSharedSchema(t *testing.T) {
	raw := []byte(`description: wait
schema:
  type: object
  properties:
    capability_request:
      description: Readiness.
      properties:
        loopback_connect:
          description: Probe ports.
          properties: {ports: {description: Ports the probe dials.}}
      required: [loopback_connect]
`)
	entry, err := toolschema.ParseEntry("wait", raw)
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	request := capabilityRequest(t, entry)
	loopback := request["properties"].(map[string]any)["loopback_connect"].(map[string]any)
	ports := loopback["properties"].(map[string]any)["ports"].(map[string]any)
	if loopback["description"] != "Probe ports." || ports["description"] != "Ports the probe dials." {
		t.Fatalf("narrowed descriptions lost: %v", loopback)
	}
	if ports["type"] != "array" || ports["uniqueItems"] != true || ports["maxItems"] == nil {
		t.Fatalf("shared ports constraints lost: %v", ports)
	}

	again, err := toolschema.ParseEntry("wait", raw)
	if err != nil {
		t.Fatalf("ParseEntry again: %v", err)
	}
	againPorts := capabilityRequest(t, again)["properties"].(map[string]any)["loopback_connect"].(map[string]any)["properties"].(map[string]any)["ports"].(map[string]any)
	againPorts["description"] = "mutated"
	if ports["description"] != "Ports the probe dials." {
		t.Fatal("parsed entries share schema maps")
	}
}

func TestDeclaringAFieldOutsideTheContractFails(t *testing.T) {
	raw := []byte("description: wait\nschema:\n  type: object\n  properties:\n    capability_request:\n      properties:\n        host_execution: {description: x}\n")
	if _, err := toolschema.ParseEntry("wait", raw); err == nil {
		t.Fatal("wait accepted a host_execution field its execution contract lacks")
	}
}

func TestToolsWithoutCapabilityRequestAreUnchanged(t *testing.T) {
	entry, err := toolschema.ParseEntry("command", []byte("description: run\nschema:\n  type: object\n  properties:\n    command: {type: string}\n"))
	if err != nil {
		t.Fatalf("ParseEntry: %v", err)
	}
	properties := entry.Schema["properties"].(map[string]any)
	if _, ok := properties["capability_request"]; ok || len(properties) != 1 {
		t.Fatalf("properties = %v", properties)
	}
}
