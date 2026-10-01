package api

import (
	"encoding/json"
	"testing"
)

func TestSourceViewCreateRejectsCrossKindFields(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"tree","source":{"kind":"effect","effect_id":"effect"}}`,
		`{"kind":"comparison","workspace_id":"workspace"}`,
		`{"kind":"comparison","source":{"kind":"text","path":"a.go","after":"text","effect_id":"effect"}}`,
		`{"kind":"comparison","source":{"kind":"unknown"}}`,
		`{"kind":"unknown"}`,
	} {
		var value SourceViewCreate
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("accepted cross-kind request: %s", raw)
		}
	}
}
func TestSourceViewCreateRetainsExplicitFalseAndEmptySource(t *testing.T) {
	raw := `{"kind":"comparison","operation_id":"request","client_id":"client","source":{"kind":"scope","file_id":"file","baseline":"presentation","mark_user_edits":false},"intent":{"mode":"changes"}}`
	var value SourceViewCreate
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("decode comparison: %v", err)
	}
	scope := value.Comparison.Source.Scope
	if scope == nil || scope.MarkUserEdits == nil || *scope.MarkUserEdits {
		t.Fatal("explicit user-edit choice was lost")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode comparison: %v", err)
	}
	var again SourceViewCreate
	if err := json.Unmarshal(encoded, &again); err != nil {
		t.Fatalf("decode encoded comparison: %v", err)
	}
	if again.Comparison.Source.Scope.MarkUserEdits == nil || *again.Comparison.Source.Scope.MarkUserEdits {
		t.Fatal("round trip lost explicit false")
	}
}
func TestSourceViewUnionRejectsMultipleVariants(t *testing.T) {
	value := SourceView{Tree: &SourceTreeView{Kind: "tree"}, Comparison: &SourceComparisonView{Kind: "comparison"}}
	if _, err := json.Marshal(value); err == nil {
		t.Fatal("encoded multiple source view kinds")
	}
}

func TestTextComparisonRequiresExplicitPresence(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"text","path":"empty.txt","before":null,"after":""}`,
		`{"kind":"text","path":"empty.txt","before":"","after":null}`,
		`{"kind":"text","path":"empty.txt","before":null,"after":null}`,
	} {
		var value SourceComparisonSelector
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatalf("decode explicit presence: %v", err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode explicit presence: %v", err)
		}
		var again SourceComparisonSelector
		if err := json.Unmarshal(encoded, &again); err != nil {
			t.Fatalf("decode presence round trip: %v", err)
		}
		if (value.Text.Before == nil) != (again.Text.Before == nil) || (value.Text.After == nil) != (again.Text.After == nil) {
			t.Fatal("round trip changed file presence")
		}
	}
	for _, raw := range []string{`{"kind":"text","path":"empty.txt","after":""}`, `{"kind":"text","path":"empty.txt","before":null}`} {
		var value SourceComparisonSelector
		if err := json.Unmarshal([]byte(raw), &value); err == nil {
			t.Fatalf("accepted unspecified presence: %s", raw)
		}
	}
}
