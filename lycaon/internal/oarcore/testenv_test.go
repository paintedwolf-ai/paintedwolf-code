package oarcore

// Shared environment and document helpers for the rejection and evaluation
// tests.

import "testing"

func sharedTestEnv(t *testing.T, mut func(*EnvironmentSpec)) *Environment {
	t.Helper()
	spec := EnvironmentSpec{
		Host:              "test.example",
		HostAnchors:       []string{"custom.anchor"},
		ActivityWindow:    10,
		Detectors:         []string{"detector://example"},
		SupportsTransform: true,
		SupportsChunk:     true,
		HostFacts: map[string]string{
			"test.example.str":      "string",
			"test.example.num":      "int",
			"test.example.ratio":    "double",
			"test.example.flag":     "bool",
			"test.example.paths":    "list<string>",
			"test.example.spans":    "list<map<string, dyn>>",
			"test.example.attrs":    "map<string, dyn>",
			"test.example.fact_len": "(string) -> int",
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
