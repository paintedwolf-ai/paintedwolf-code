package oarcore

// Shared environment and document helpers for the rejection and evaluation
// tests.

import "testing"

func sharedTestEnv(t *testing.T, mut func(*EnvironmentSpec)) *Environment {
	t.Helper()
	spec := EnvironmentSpec{
		Host:           "testhost",
		HostAnchors:    []string{"custom.anchor"},
		ActivityWindow: 10,
		Detectors:      []string{"detector://example"},
		SupportsChunk:  true,
		HostFacts: map[string]string{
			"testhost.str":      "string",
			"testhost.num":      "int",
			"testhost.ratio":    "double",
			"testhost.flag":     "bool",
			"testhost.paths":    "list<string>",
			"testhost.spans":    "list<map<string, dyn>>",
			"testhost.attrs":    "map<string, dyn>",
			"testhost.fact_len": "(string) -> int",
		},
	}
	if mut != nil {
		mut(&spec)
	}
	env, err := NewEnvironment(spec)
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	return env
}

func ruleDoc(mut func(map[string]any)) map[string]any {
	doc := map[string]any{
		"spec_version": "1.0",
		"id":           "SOME_RULE",
		"kind":         "policy",
		"on":           "custom.anchor",
		"selector":     map[string]any{},
		"effect":       "nudge",
	}
	if mut != nil {
		mut(doc)
	}
	return doc
}

func hintDoc(mut func(map[string]any)) map[string]any {
	doc := map[string]any{
		"spec_version": "1.0",
		"id":           "SOME_HINT",
		"kind":         "hint",
		"message":      "a static message",
	}
	if mut != nil {
		mut(doc)
	}
	return doc
}
