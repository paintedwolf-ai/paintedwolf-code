package pongoplain

import (
	"slices"
	"testing"
)

func TestExprAtomsPrintAsEvaluableSource(t *testing.T) {
	expr, err := ParseExpression(`a and not (b.c or d == "x") and e|length > 2 and f not in g`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, atom := range expr.Atoms() {
		got = append(got, atom.Source())
	}
	want := []string{"a", "b.c", `d == "x"`, "e|length > 2", "f not in g"}
	if !slices.Equal(got, want) {
		t.Fatalf("atoms = %q, want %q", got, want)
	}
	all := map[string]bool{"a": true, "e|length > 2": true, "f not in g": true}
	if !expr.Decide(all) {
		t.Fatal("Decide: a and not (false or false) and true and true should hold")
	}
	all["b.c"] = true
	if expr.Decide(all) {
		t.Fatal("Decide: a true disjunct under not must fail the guard")
	}
}

func TestObservedConditionsRecordEveryAtomPerArm(t *testing.T) {
	tpl, err := Compile(`{% if a and not (b or n == 2) %}x{% elif a %}y{% endif %}`)
	if err != nil {
		t.Fatal(err)
	}
	rec := &BranchRecorder{ObserveConditions: true}
	ActiveBranchRecorder.Store(rec)
	defer ActiveBranchRecorder.Store(nil)
	for _, vars := range []map[string]any{
		{"a": true, "b": false, "n": 2},
		{"a": false, "b": true, "n": 1},
		{"a": true, "b": false, "n": 2},
	} {
		if _, err := ExecuteDetached(tpl, vars); err != nil {
			t.Fatal(err)
		}
	}
	var ifHit, elifHit *ConditionHit
	for _, hit := range rec.Conditions {
		switch hit.Kind {
		case BranchIf:
			ifHit = hit
		case BranchElif:
			elifHit = hit
		}
	}
	if ifHit == nil || elifHit == nil {
		t.Fatalf("conditions = %+v", rec.Conditions)
	}
	if want := []string{"a", "b", "n == 2"}; !slices.Equal(ifHit.Atoms, want) {
		t.Fatalf("if atoms = %q, want %q", ifHit.Atoms, want)
	}
	// The short-circuited atoms of the second render are read anyway.
	wantVectors := []ConditionVector{
		{Atoms: []Truth{True, False, True}, Decision: false, Count: 2},
		{Atoms: []Truth{False, True, False}, Decision: false, Count: 1},
	}
	if len(ifHit.Vectors) != len(wantVectors) {
		t.Fatalf("if vectors = %d, want %d", len(ifHit.Vectors), len(wantVectors))
	}
	for i, v := range ifHit.Vectors {
		if !slices.Equal(v.Atoms, wantVectors[i].Atoms) || v.Decision != wantVectors[i].Decision || v.Count != wantVectors[i].Count {
			t.Fatalf("if vector %d = %+v, want %+v", i, *v, wantVectors[i])
		}
	}
	// The elif arm is only evaluated when the if arm did not win.
	total := 0
	for _, v := range elifHit.Vectors {
		total += v.Count
	}
	if total != 3 {
		t.Fatalf("elif evaluations = %d, want 3", total)
	}
	for id := range rec.Hits {
		if hit := rec.Hits[id]; hit.Template != ifHit.Template {
			t.Fatalf("atom replay recorded a branch in %s", hit.Template)
		}
	}
}
