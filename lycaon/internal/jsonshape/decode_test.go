package jsonshape

import (
	"errors"
	"reflect"
	"testing"
)

type ask struct {
	Do     string `json:"do"`
	Effort string `json:"effort"`
}

type finding struct {
	ID      string            `json:"id,omitempty"`
	Title   string            `json:"title"`
	Line    int               `json:"line,omitempty"`
	Answers map[string]string `json:"answers,omitempty"`
}

type report struct {
	Findings []finding `json:"findings,omitempty"`
	Ask      *ask      `json:"ask,omitempty"`
	Limits   []string  `json:"limits,omitempty"`
	Skipped  string    `json:"-"`
}

// Declared members decode; undeclared ones are named by path and pattern,
// with the member that holds them.
func TestDecodeKeepsDeclaredMembersAndNamesTheRest(t *testing.T) {
	var got report
	issues, err := Decode([]byte(`{
		"findings": [
			{"id": "a", "title": "A", "ask": {"do": "x"}},
			{"id": "b", "title": "B", "statement": "s", "ask": {"do": "y"}}
		],
		"ask": {"do": "Fix it", "effort": "small"},
		"Skipped": "no"
	}`), &got)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := report{
		Findings: []finding{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}},
		Ask:      &ask{Do: "Fix it", Effort: "small"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded = %+v, want %+v", got, want)
	}
	wantIssues := []Issue{
		{Path: "Skipped", Pattern: "Skipped", Name: "Skipped", Kind: Unknown},
		{Path: "findings[0].ask", Pattern: "findings[].ask", Parent: "findings[]", Name: "ask", Kind: Unknown},
		{Path: "findings[1].ask", Pattern: "findings[].ask", Parent: "findings[]", Name: "ask", Kind: Unknown},
		{Path: "findings[1].statement", Pattern: "findings[].statement", Parent: "findings[]", Name: "statement", Kind: Unknown},
	}
	if !reflect.DeepEqual(issues, wantIssues) {
		t.Fatalf("issues = %+v\nwant %+v", issues, wantIssues)
	}
}

// A member of the wrong type is left out and named with the type it needs;
// a wrong array element is dropped from its array.
func TestDecodeLeavesOutMismatchedMembers(t *testing.T) {
	var got report
	issues, err := Decode([]byte(`{
		"findings": [{"title": "A", "line": "12", "answers": {"reachable": 1, "outcome": "none"}}, "B"],
		"ask": "Fix it",
		"limits": ["x", 2]
	}`), &got)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := report{
		Findings: []finding{{Title: "A", Answers: map[string]string{"outcome": "none"}}},
		Limits:   []string{"x"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded = %+v, want %+v", got, want)
	}
	wantIssues := []Issue{
		{Path: "ask", Pattern: "ask", Name: "ask", Kind: Mismatch, Want: "object"},
		{Path: "findings[0].answers.reachable", Pattern: "findings[].answers.reachable", Parent: "findings[].answers", Name: "reachable", Kind: Mismatch, Want: "string"},
		{Path: "findings[0].line", Pattern: "findings[].line", Parent: "findings[]", Name: "line", Kind: Mismatch, Want: "integer"},
		{Path: "findings[1]", Pattern: "findings[]", Parent: "findings", Kind: Mismatch, Want: "object"},
		{Path: "limits[1]", Pattern: "limits[]", Parent: "limits", Kind: Mismatch, Want: "string"},
	}
	if !reflect.DeepEqual(issues, wantIssues) {
		t.Fatalf("issues = %+v\nwant %+v", issues, wantIssues)
	}
}

func TestDecodeRefusesInputThatCannotFillTheDestination(t *testing.T) {
	var got report
	if _, err := Decode([]byte(`{"findings": [`), &got); !errors.Is(err, ErrNotJSON) {
		t.Fatalf("truncated input: err = %v, want ErrNotJSON", err)
	}
	if _, err := Decode([]byte(`["findings"]`), &got); !errors.Is(err, ErrWrongType) {
		t.Fatalf("array for an object: err = %v, want ErrWrongType", err)
	}
}

func TestFieldsListsDeclaredMembers(t *testing.T) {
	if got := Fields(reflect.TypeFor[*report]()); !reflect.DeepEqual(got, []string{"findings", "ask", "limits"}) {
		t.Fatalf("Fields = %v", got)
	}
}
