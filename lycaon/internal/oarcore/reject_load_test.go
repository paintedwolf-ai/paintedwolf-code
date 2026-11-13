package oarcore

// Table tests for LoadDocument: every document-model rejection names the
// field, identifier, or fact at fault.

import (
	"strings"
	"testing"
)

func TestLoadDocumentRejectsRuleFaults(t *testing.T) {
	env := sharedTestEnv(t, nil)
	cases := []struct {
		name string
		mut  func(map[string]any)
		want string
	}{
		{"missing spec_version", func(d map[string]any) { delete(d, "spec_version") }, `missing required field "spec_version"`},
		{"bad spec_version spelling", func(d map[string]any) { d["spec_version"] = "01.0" }, "must be a version string"},
		{"wrong major", func(d map[string]any) { d["spec_version"] = "2.0" }, "names a major version this engine does not implement"},
		{"wrong minor", func(d map[string]any) { d["spec_version"] = "1.99" }, "minor version above this engine's own"},
		{"undefined field", func(d map[string]any) { d["bogus"] = 1 }, `carries undefined field "bogus"`},
		{"missing id", func(d map[string]any) { delete(d, "id") }, `missing required field "id"`},
		{"bad id", func(d map[string]any) { d["id"] = "some_rule" }, `field "id" does not match`},
		{"bad namespace", func(d map[string]any) { d["namespace"] = "Bad Scope" }, "is not a publisher scope"},
		{"bad kind", func(d map[string]any) { d["kind"] = "bogus" }, `field "kind" is`},
		{"bad effect", func(d map[string]any) { d["effect"] = "bogus" }, `field "effect" is`},
		{"bad on_error", func(d map[string]any) { d["on_error"] = "bogus" }, `field "on_error" must be fail_closed`},
		{"mandatory not bool", func(d map[string]any) { d["mandatory"] = "yes" }, `field "mandatory" must be a boolean`},
		{"undefined on_fire action", func(d map[string]any) { d["on_fire"] = []any{"bogus"} }, `names undefined side-effect`},
		{"on_fire not a list", func(d map[string]any) { d["on_fire"] = "increment_breaker" }, "must be a list of strings"},
		{"bad severity", func(d map[string]any) { d["severity"] = "bogus" }, `field "severity" must be one of`},
		{"empty severity", func(d map[string]any) { d["severity"] = "" }, `field "severity" must be one of`},
		{"bad enforcement", func(d map[string]any) { d["enforcement"] = "bogus" }, `field "enforcement" must be one of`},
		{"empty enforcement", func(d map[string]any) { d["enforcement"] = "" }, `field "enforcement" must be one of`},
		{"bad profile", func(d map[string]any) { d["profile"] = "bogus" }, `field "profile" must be one of`},
		{"title not string", func(d map[string]any) { d["title"] = 5 }, `field "title" must be a string`},
		{"references not object", func(d map[string]any) { d["references"] = "x" }, `field "references" must be an object`},
		{"references undefined member", func(d map[string]any) { d["references"] = map[string]any{"bogus": []any{"x"}} }, "carries undefined member"},
		{"references member not list", func(d map[string]any) { d["references"] = map[string]any{"mitre_atlas": 5} }, "must be a list of strings"},
		{"context_schema not object", func(d map[string]any) { d["context_schema"] = 5 }, `must be an object`},
		{"evidence not object", func(d map[string]any) { d["evidence"] = 5 }, `must be an object`},
		{"scenarios not list", func(d map[string]any) { d["scenarios"] = map[string]any{} }, `field "scenarios" must be a list of objects`},
		{"scenarios member not object", func(d map[string]any) { d["scenarios"] = []any{5} }, `field "scenarios" must be a list of objects`},
		{"detector on policy", func(d map[string]any) { d["detector"] = map[string]any{"ref": "detector://example"} }, "must not carry"},
		{"detector missing on detector kind", func(d map[string]any) { d["kind"] = "detector" }, "must carry"},
		{"detector undefined member", func(d map[string]any) { d["kind"] = "detector"; d["detector"] = map[string]any{"nope": 1} }, `field "detector" carries undefined member`},
		{"detector malformed ref", func(d map[string]any) { d["kind"] = "detector"; d["detector"] = map[string]any{"ref": "nope"} }, "must be a detector:// reference"},
		{"detector unresolvable", func(d map[string]any) {
			d["kind"] = "detector"
			d["detector"] = map[string]any{"ref": "detector://other"}
		}, "cannot resolve"},
		{"on not a string", func(d map[string]any) { d["on"] = 5 }, `field "on" must name exactly one anchor`},
		{"on unknown anchor", func(d map[string]any) { d["on"] = "unknown.anchor" }, "cannot resolve"},
		{"requires_facts malformed name", func(d map[string]any) { d["requires_facts"] = []any{"1bad"} }, "holds malformed name"},
		{"requires_facts unknown fact", func(d map[string]any) { d["requires_facts"] = []any{"test.example.nofact"} }, "which this host does not provide"},
		{"selector not object", func(d map[string]any) { d["selector"] = 5 }, `field "selector" must be an object`},
		{"selector unknown clause", func(d map[string]any) { d["selector"] = map[string]any{"bogus": "x"} }, "which the selector schema does not define"},
		{"selector bad surface value", func(d map[string]any) { d["selector"] = map[string]any{"surface": "bogus"} }, "must be coordinator, worker, phase, or null"},
		{"selector surface not string", func(d map[string]any) { d["selector"] = map[string]any{"surface": 5} }, "must be a string or null"},
		{"selector bad principal role", func(d map[string]any) { d["selector"] = map[string]any{"principal": "bogus"} }, "names role"},
		{"selector principal not string", func(d map[string]any) { d["selector"] = map[string]any{"principal": 5} }, "must be a role or a list"},
		{"selector ledger not object", func(d map[string]any) { d["selector"] = map[string]any{"ledger": 5} }, `must be an object`},
		{"selector ledger untyped fact", func(d map[string]any) {
			d["selector"] = map[string]any{"ledger": map[string]any{"test.example.num": "x"}}
		}, "neither string nor list<string>"},
		{"selector ledger bad values", func(d map[string]any) {
			d["selector"] = map[string]any{"ledger": map[string]any{"test.example.str": 5}}
		}, "must be a list of strings"},
		{"when not string", func(d map[string]any) { d["when"] = 5 }, `field "when" must be a string`},
		{"when parse error", func(d map[string]any) { d["when"] = `tool ==` }, "condition:"},
		{"when not bool", func(d map[string]any) { d["when"] = `test.example.str` }, "produces string, want bool"},
		{"copy member not string", func(d map[string]any) { d["what"] = 5 }, `field "what" must be a string`},
		{"copy unknown fact", func(d map[string]any) { d["what"] = "{{test.example.nofact}}" }, "is not a declared fact"},
		{"copy non-interpolatable type", func(d map[string]any) {
			d["what"] = "{{test.example.attrs}}"
			d["requires_facts"] = []any{"test.example.attrs"}
		}, "whose type is map<string, dyn>"},
		{"copy unreached fact", func(d map[string]any) { d["what"] = "{{test.example.str}}" }, "does not reach through requires_facts"},
		{"copy malformed construct", func(d map[string]any) { d["what"] = "{{test.example.str" }, "carries construct"},
		{"flow not a list", func(d map[string]any) { d["flow"] = "step" }, "must be a list of strings"},
		{"empty flow", func(d map[string]any) { d["flow"] = []any{} }, `carries an empty "flow"`},
		{"flow above window", func(d map[string]any) { d["flow"] = []any{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} }, "above the published activity window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := LoadDocument(ruleDoc(tc.mut), env)
			if err == nil {
				t.Fatalf("LoadDocument accepted fault %q", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("fault %q: got %q, want substring %q", tc.name, err.Error(), tc.want)
			}
		})
	}
}

// Stream chunk and effect transform require an engine that supports them.
func TestLoadDocumentRejectsUnsupportedEngine(t *testing.T) {
	env := sharedTestEnv(t, func(s *EnvironmentSpec) { s.SupportsTransform = false; s.SupportsChunk = false })
	for _, tc := range []struct {
		name string
		mut  func(map[string]any)
		want string
	}{
		{"chunk unsupported", func(d map[string]any) { d["stream"] = "chunk" }, "requests stream chunk"},
		{"transform unsupported", func(d map[string]any) { d["effect"] = "transform" }, "has effect transform"},
	} {
		_, _, err := LoadDocument(ruleDoc(tc.mut), env)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want substring %q", tc.name, err, tc.want)
		}
	}
}

func TestLoadDocumentRuleShape(t *testing.T) {
	env := sharedTestEnv(t, nil)
	doc := ruleDoc(func(d map[string]any) {
		d["namespace"] = "acme.rules"
		d["when"] = `tool == "read"`
		d["requires_facts"] = []any{"test.example.str"}
		d["what"] = "saw {{test.example.str}}"
		d["on_fire"] = []any{"increment_breaker"}
		d["enforcement"] = "monitor"
		d["severity"] = "warn"
		d["tools"] = []any{"read"}
	})
	rule, hint, err := LoadDocument(doc, env)
	if err != nil || hint != nil {
		t.Fatalf("LoadDocument: %v hint=%v", err, hint)
	}
	if rule.Qualified != "acme.rules/SOME_RULE" {
		t.Fatalf("Qualified = %q", rule.Qualified)
	}
	if rule.Enforcement != "monitor" || rule.Severity != "warn" || rule.OnError != "fail_closed" || rule.Stream != "buffered" {
		t.Fatalf("scalars: %+v", rule)
	}
	if !rule.Refs["tool"] || !rule.Refs["test.example.str"] {
		t.Fatalf("refs: %v", rule.Refs)
	}
	if len(rule.Selector) != 1 || rule.Selector[0].Dimension != "tools" || rule.Selector[0].Values[0] != "read" {
		t.Fatalf("selector: %+v", rule.Selector)
	}

	// The deprecated top-level tools stands for selector.tools.
	copied := ruleDoc(func(d map[string]any) { d["tools"] = []any{"write"} })
	rule2, _, err := LoadDocument(copied, env)
	if err != nil {
		t.Fatalf("top-level tools: %v", err)
	}
	if len(rule2.Selector) != 1 || rule2.Selector[0].Dimension != "tools" {
		t.Fatalf("tools selector: %+v", rule2.Selector)
	}

	// A typed []string is accepted where a list of strings is required.
	typed := ruleDoc(func(d map[string]any) { d["on_fire"] = []string{"increment_breaker"} })
	if _, _, err := LoadDocument(typed, env); err != nil {
		t.Fatalf("typed on_fire: %v", err)
	}
}

func TestLoadDocumentRejectsHintFaults(t *testing.T) {
	env := sharedTestEnv(t, nil)
	cases := []struct {
		name string
		mut  func(map[string]any)
		want string
	}{
		{"undefined field", func(d map[string]any) { d["bogus"] = 1 }, `carries undefined field "bogus"`},
		{"bad id", func(d map[string]any) { d["id"] = "some_hint" }, `field "id" does not match`},
		{"bad namespace", func(d map[string]any) { d["namespace"] = 5 }, "is not a publisher scope"},
		{"title not string", func(d map[string]any) { d["title"] = 5 }, `field "title" must be a string`},
		{"emit not string", func(d map[string]any) { d["emit"] = 5 }, `field "emit" must be a string`},
		{"bad severity", func(d map[string]any) { d["severity"] = "bogus" }, `field "severity" must be one of`},
		{"references undefined member", func(d map[string]any) { d["references"] = map[string]any{"bogus": []any{"x"}} }, "carries undefined member"},
		{"copy member not string", func(d map[string]any) { d["message"] = 5 }, `field "message" must be a string`},
		{"copy carries binding", func(d map[string]any) { d["message"] = "{{tool}}" }, "carries a binding"},
		{"no copy", func(d map[string]any) { delete(d, "message") }, "carries no copy field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := LoadDocument(hintDoc(tc.mut), env)
			if err == nil {
				t.Fatalf("LoadDocument accepted hint fault %q", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("fault %q: got %q, want substring %q", tc.name, err.Error(), tc.want)
			}
		})
	}

	_, hint, err := LoadDocument(hintDoc(nil), env)
	if err != nil || hint == nil {
		t.Fatalf("valid hint: %v", err)
	}

	_, _, err = LoadDocument(42, env)
	if err == nil || !strings.Contains(err.Error(), "is not a document") {
		t.Fatalf("non-object document: %v", err)
	}

	_, err = LoadRuleDocument(hintDoc(nil), env)
	if err == nil || !strings.Contains(err.Error(), "is not a rule") {
		t.Fatalf("hint through LoadRuleDocument: %v", err)
	}
}

func TestLoadDocumentConditionNodeLimit(t *testing.T) {
	env := sharedTestEnv(t, func(spec *EnvironmentSpec) { spec.ExpressionNodesMax = 2 })
	_, _, err := LoadDocument(ruleDoc(func(d map[string]any) { d["when"] = `true && true && true` }), env)
	if err == nil || !strings.Contains(err.Error(), "above this engine's limit") {
		t.Fatalf("node limit: %v", err)
	}
}
