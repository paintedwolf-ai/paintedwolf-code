package bindings_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/mcp/bindings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"gopkg.in/yaml.v3"
)

func TestLoadEqualsOnNonBoolFails(t *testing.T) {
	raw := []byte(`id: bad
provider_id: s
tool_name: t
schema: {type: object}
fields:
  - key: x
    type: string
    path: /x
    equals: started
`)
	_, err := bindings.LoadBytes("bad", raw)
	if err == nil {
		t.Fatal("expected equals/type error")
	}
}

func TestApplySuccess(t *testing.T) {
	list := extpackstest.MCPBindings(t)
	result := `{"id":"ISSUE-1","count":3,"state":{"type":"started","name":"In Progress"}}`
	matched, fields, err := bindings.Apply(list, "fixture", "echo", result)
	testutil.FailErr(t, "Apply", err)
	if !matched {
		t.Fatal("expected match")
	}
	if fields["issue_id"] != "ISSUE-1" {
		t.Fatalf("issue_id=%v", fields["issue_id"])
	}
	if fields["state_type"] != "started" {
		t.Fatalf("state_type=%v", fields["state_type"])
	}
	if fields["is_started"] != true {
		t.Fatalf("is_started=%v", fields["is_started"])
	}
	if fields["item_count"] != int64(3) {
		t.Fatalf("item_count=%v (%T)", fields["item_count"], fields["item_count"])
	}
}

func TestApplyEqualsFalse(t *testing.T) {
	list := extpackstest.MCPBindings(t)
	result := `{"id":"ISSUE-1","count":1,"state":{"type":"completed","name":"Done"}}`
	matched, fields, err := bindings.Apply(list, "fixture", "echo", result)
	testutil.FailErr(t, "Apply", err)
	if !matched {
		t.Fatal("expected match")
	}
	if fields["is_started"] != false {
		t.Fatalf("is_started=%v", fields["is_started"])
	}
}

func TestApplyInvalidJSON(t *testing.T) {
	list := extpackstest.MCPBindings(t)
	matched, fields, err := bindings.Apply(list, "fixture", "echo", "not-json")
	testutil.FailErr(t, "Apply", err)
	if matched || fields != nil {
		t.Fatalf("matched=%v fields=%v", matched, fields)
	}
}

func TestApplySchemaFail(t *testing.T) {
	list := extpackstest.MCPBindings(t)
	// missing required id/state
	matched, fields, err := bindings.Apply(list, "fixture", "echo", `{"count":1}`)
	testutil.FailErr(t, "Apply", err)
	if matched || fields != nil {
		t.Fatalf("matched=%v fields=%v", matched, fields)
	}
}

func TestLoadInvalidSchema(t *testing.T) {
	raw := []byte(`id: bad_schema
provider_id: s
tool_name: t
schema:
  type: not-a-real-type
fields: []
`)
	// jsonschema may accept unknown type strings depending on draft; force broken schema.
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		testutil.FailErr(t, "unmarshal YAML document", err)
	}
	doc["schema"] = true // not an object schema document
	broken, err := yaml.Marshal(doc)
	testutil.FailErr(t, "marshal", err)
	_, err = bindings.LoadBytes("bad_schema", broken)
	if err == nil {
		t.Fatal("expected schema compile error")
	}
}
