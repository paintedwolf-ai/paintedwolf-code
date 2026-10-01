package scan

import "testing"

func TestParseObligationParamsDefaultsAndGates(t *testing.T) {
	plain, err := parseObligationParams(map[string]any{"categories": []any{"security"}})
	if err != nil || plain.Full || plain.Gate != obligationGateNoNew || len(plain.Categories) != 1 {
		t.Fatalf("plain params = %+v, %v", plain, err)
	}
	full, err := parseObligationParams(map[string]any{"categories": []any{"security"}, "full": true})
	if err != nil || !full.Full || full.Gate != obligationGateComplete {
		t.Fatalf("full params = %+v, %v", full, err)
	}
	if _, err := parseObligationParams(map[string]any{"categories": []any{"security"}, "gate": "complete"}); err == nil {
		t.Fatal("a complete gate without a full pass must be rejected")
	}
	if _, err := parseObligationParams(map[string]any{"categories": []any{"security"}, "gate": "someday"}); err == nil {
		t.Fatal("an unknown gate must be rejected")
	}
	if _, err := parseObligationParams(map[string]any{"categories": []any{"security"}, "paths": []any{"x"}}); err == nil {
		t.Fatal("an unknown param must be rejected")
	}
}
