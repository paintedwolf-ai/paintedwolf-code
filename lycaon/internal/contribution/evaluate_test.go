package contribution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type conformanceFile struct {
	Cases []conformanceCase `json:"cases"`
}

type conformanceCase struct {
	Name       string          `json:"name"`
	Condition  json.RawMessage `json:"condition"`
	Facts      map[string]bool `json:"facts"`
	Expect     bool            `json:"expect"`
	ExpectHost string          `json:"expect_host"`
}

type jsonCondition struct {
	All  []jsonCondition `json:"all,omitempty"`
	Any  []jsonCondition `json:"any,omitempty"`
	Not  *jsonCondition  `json:"not,omitempty"`
	Fact string          `json:"fact,omitempty"`
	Is   string          `json:"is,omitempty"`
}

func (j jsonCondition) condition() Condition {
	out := Condition{Fact: j.Fact, Is: j.Is}
	for _, child := range j.All {
		out.All = append(out.All, child.condition())
	}
	for _, child := range j.Any {
		out.Any = append(out.Any, child.condition())
	}
	if j.Not != nil {
		not := j.Not.condition()
		out.Not = &not
	}
	return out
}

func TestConditionConformance(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "condition-conformance.json"))
	testutil.FailErr(t, "read conformance fixtures", err)
	var file conformanceFile
	testutil.FailErr(t, "decode conformance fixtures", json.Unmarshal(data, &file))
	if len(file.Cases) == 0 {
		t.Fatal("no conformance cases")
	}

	for _, tc := range file.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			var cond *Condition
			if string(tc.Condition) != "null" && len(tc.Condition) > 0 {
				var decoded jsonCondition
				testutil.FailErr(t, "decode condition", json.Unmarshal(tc.Condition, &decoded))
				c := decoded.condition()
				cond = &c
				testutil.FailErr(t, "fixture condition must validate",
					validateCondition(c, conditionScope{
						RequirementExists:    func(ID) bool { return true },
						BooleanConfiguration: func(ID) (bool, bool) { return true, true },
					}))
			}
			lookup := func(fact, operand string) bool {
				key := fact
				if operand != "" {
					key = fact + "=" + operand
				}
				return tc.Facts[key]
			}
			if got := EvaluateCondition(cond, lookup); got != tc.Expect {
				t.Fatalf("EvaluateCondition = %v want %v", got, tc.Expect)
			}
			want := map[string]HostVerdict{
				"true": HostTrue, "false": HostFalse, "unknown": HostUnknown,
			}[tc.ExpectHost]
			if got := EvaluateHostCondition(cond, lookup); got != want {
				t.Fatalf("EvaluateHostCondition = %v want %v (%s)", got, want, tc.ExpectHost)
			}
		})
	}
}
