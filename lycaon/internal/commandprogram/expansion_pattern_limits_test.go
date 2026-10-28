package commandprogram

import (
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestParameterPatternRefusesBeforeBuildingOversizedResult(t *testing.T) {
	memory := testMemory(t)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	scope := testScope(t, memory, []string{"V=" + strings.Repeat("a", 32), "R=" + strings.Repeat("b", 16)})
	expander.limits.valueBytes = 128
	fields, err := expander.ExpandFields(t.Context(), scope, parsedWords(t, `: "${V//a/$R}"`)[1])
	var refused *ExpansionError
	if !errors.As(err, &refused) || refused.Limit != "variable_value_bytes" || fields != nil {
		t.Fatalf("oversized replacement: fields=%v error=%v", fields, err)
	}
	if memory.InUse().Working != 0 {
		t.Fatalf("replacement retained working storage: %+v", memory.InUse())
	}
	if value, _ := scope.Variable("V"); value != strings.Repeat("a", 32) {
		t.Fatal("refusal changed source variable")
	}
}

func TestParameterPatternAdmitsSourceAndStopsAtWorkBoundary(t *testing.T) {
	t.Run("source representation", func(t *testing.T) {
		memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32768, State: 1 << 20})
		testutil.FailErr(t, "create working memory", err)
		expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
		scope := testScope(t, memory, []string{"V=" + strings.Repeat("x", 8192)})
		fields, err := expander.ExpandFields(t.Context(), scope, parsedWords(t, `: "${V#x}"`)[1])
		var refused *toolcontract.ProgramMemoryLimitError
		if !errors.As(err, &refused) || refused.Account != "working" || fields != nil {
			t.Fatalf("source representation: fields=%v error=%v", fields, err)
		}
		if memory.InUse().Working != 0 || memory.Peak().Working > 32768 {
			t.Fatalf("source admission: used=%+v peak=%+v", memory.InUse(), memory.Peak())
		}
	})
	t.Run("work boundary", func(t *testing.T) {
		memory := testMemory(t)
		expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
		scope := testScope(t, memory, []string{"V=" + strings.Repeat("x", 1024)})
		expander.limits.maximum = maps.Clone(expander.limits.maximum)
		expander.limits.maximum["regex_work_steps"] = 3000
		fields, err := expander.ExpandFields(t.Context(), scope, parsedWords(t, `: "${V//z/y}"`)[1])
		var refused *ExpansionError
		if !errors.As(err, &refused) || refused.Limit != "regex_work_steps" || fields != nil {
			t.Fatalf("work boundary: fields=%v error=%v", fields, err)
		}
		if memory.InUse().Working != 0 {
			t.Fatalf("work refusal retained working storage: %+v", memory.InUse())
		}
	})
}

func TestParameterCaseConversionAdmitsResultBeforeAllocation(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 256, State: 1 << 20})
	testutil.FailErr(t, "small case working budget", err)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	scope := testScope(t, memory, []string{"V=" + strings.Repeat("x", 512)})
	_, err = expander.ExpandFields(t.Context(), scope, parsedWords(t, `: "${V^^}"`)[1])
	var exhausted *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &exhausted) || exhausted.Account != "working" {
		t.Fatalf("case conversion error=%v, want working memory refusal", err)
	}
	if used := memory.InUse(); used.Working != 0 {
		t.Fatalf("refused case conversion retained working storage: %+v", used)
	}
}
