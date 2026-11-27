package independence

import (
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func andOf(_ string, v []Truth) (bool, error) {
	for _, t := range v {
		if t != True {
			return false, nil
		}
	}
	return true, nil
}

func parityOf(_ string, v []Truth) (bool, error) {
	odd := false
	for _, t := range v {
		odd = odd != (t == True)
	}
	return odd, nil
}

func orOf(_ string, v []Truth) (bool, error) {
	for _, t := range v {
		if t == True {
			return true, nil
		}
	}
	return false, nil
}

// Two atoms over one operation cannot both hold; the joint rules out the
// only pair that would witness either, so both are excluded, and an
// observation of the impossible vector reports the joint as stale.
func TestJointConstraintExcludesAndDetectsContradiction(t *testing.T) {
	g := Guard{Site: "op", Source: `op == "a" || op == "b"`, Atoms: []string{`op == "a"`, `op == "b"`},
		Vectors: []Vector{{Atoms: []Truth{False, False}, Decision: false}}}
	cons := Constraints{Joints: []Joint{{
		Atoms: []string{`op == "a"`, `op == "b"`}, Allowed: [][]Truth{{False, False}, {True, False}, {False, True}}, Source: "expression",
	}}}
	res, err := Analyze([]Guard{g}, orOf, cons)
	contractcheck.FailErr(t, "Analyze failed", err)
	if len(res.Gaps) != 2 || len(res.Gaps[0].Needs) != 1 || res.Gaps[0].Needs[0][`op == "b"`] != False {
		t.Fatalf("gaps = %+v", res.Gaps)
	}

	g.Atoms = []string{`op == "a"`, `op == "b"`}
	g.Source = `op == "a" && op == "b"`
	res, err = Analyze([]Guard{g}, andOf, cons)
	contractcheck.FailErr(t, "Analyze failed", err)
	if len(res.Gaps) != 0 || len(res.Exclusions) != 2 || !strings.Contains(res.Exclusions[0].Reason, "joint") {
		t.Fatalf("result = %+v", res)
	}

	g.Vectors = append(g.Vectors, Vector{Atoms: []Truth{True, True}, Decision: true})
	res, err = Analyze([]Guard{g}, andOf, cons)
	contractcheck.FailErr(t, "Analyze failed", err)
	if len(res.Contradicted) != 1 || !strings.Contains(res.Contradicted[0], "joint") {
		t.Fatalf("contradicted = %v", res.Contradicted)
	}
}

// A gap keeps the assignments that would witness it: one for a conjunction
// or disjunction, and at most maxNeeds when every assignment would.
func TestGapNeedsListTheWitnessingAssignments(t *testing.T) {
	var atoms []string
	for i := range 7 {
		atoms = append(atoms, string(rune('a'+i)))
	}
	res, err := Analyze([]Guard{{Site: "or", Atoms: atoms}}, orOf, Constraints{})
	contractcheck.FailErr(t, "Analyze failed", err)
	for _, g := range res.Gaps {
		if len(g.Needs) != 1 || len(g.Missing) != 1 {
			t.Fatalf("gap = %+v", g)
		}
	}
	res, err = Analyze([]Guard{{Site: "and", Atoms: atoms}}, andOf, Constraints{})
	contractcheck.FailErr(t, "Analyze failed", err)
	if len(res.Gaps) != 7 || len(res.Gaps[0].Needs) != 1 {
		t.Fatalf("gaps = %+v", res.Gaps)
	}
	res, err = Analyze([]Guard{{Site: "parity", Atoms: atoms}}, parityOf, Constraints{})
	contractcheck.FailErr(t, "Analyze failed", err)
	if g := res.Gaps[0]; len(g.Needs) != maxNeeds || len(g.Missing) != maxListed+1 {
		t.Fatalf("gap = %+v", g)
	}
}
