package commandprogram

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func expansionLimitsFixture(t *testing.T) expansionLimits {
	t.Helper()
	limits, err := catalogExpansionLimits()
	testutil.FailErr(t, "catalog expansion limits", err)
	return limits
}

// expandAt expands one word against a scope holding V and reports the field
// count or the limit that refused it, checking the refusal's span.
func expandAt(t *testing.T, files commandsurface.GlobFiles, source, value string) (int, string) {
	t.Helper()
	memory := testMemory(t)
	expander, _ := fixtureExpander(t, files, memory)
	scope := testScope(t, memory, nil)
	scope.values["V"] = variable{value: value, set: true, release: func() {}}
	word := parsedWords(t, source)[1]
	fields, err := expander.ExpandFields(t.Context(), scope, word)
	if err == nil {
		return len(fields), ""
	}
	var refused *ExpansionError
	if !errors.As(err, &refused) || refused.Code != codeLimitExceeded || refused.Span != word.Span || refused.Observed <= refused.Maximum {
		t.Fatalf("%s: err = %v, want a located limit refusal", source, err)
	}
	return 0, refused.Limit
}

func TestExpandedFieldBoundaries(t *testing.T) {
	limits := expansionLimitsFixture(t)
	files := commandsurface.OSGlobFiles{Dir: t.TempDir()}
	cases := []struct {
		name, source, value string
		fields              int
		limit               string
	}{
		{"fields at limit", `: $V`, strings.Repeat("x ", int(limits.fields)), int(limits.fields), ""},
		{"fields over limit", `: $V`, strings.Repeat("x ", int(limits.fields)+1), 0, "expanded_fields"},
		{"argument at limit", `: "$V"`, strings.Repeat("a", int(limits.argumentBytes)), 1, ""},
		{"argument over limit", `: "$V"`, strings.Repeat("a", int(limits.argumentBytes)+1), 0, "argument_bytes"},
		{"argv at limit", `: $V`, strings.Repeat(strings.Repeat("a", int(limits.argumentBytes))+" ", int(limits.argvBytes/limits.argumentBytes)), int(limits.argvBytes / limits.argumentBytes), ""},
		{"argv over limit", `: $V`, strings.Repeat(strings.Repeat("a", int(limits.argumentBytes))+" ", int(limits.argvBytes/limits.argumentBytes)) + "b", 0, "argv_bytes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fields, limit := expandAt(t, files, c.source, c.value)
			if fields != c.fields || limit != c.limit {
				t.Fatalf("fields=%d limit=%q, want %d %q", fields, limit, c.fields, c.limit)
			}
		})
	}
}

func TestGlobBudgetsAreCumulativeForTheProgram(t *testing.T) {
	limits := expansionLimitsFixture(t)
	matches := int(limits.globMatches)
	files := memoryFiles{"": nil, "a": namedEntries("m", matches-1), "b": namedEntries("m", 1), "c": namedEntries("m", 1)}
	memory := testMemory(t)
	expander, _ := fixtureExpander(t, files, memory)
	scope := testScope(t, memory, nil)
	for _, source := range []string{`: a/*`, `: b/*`} {
		fields, err := expander.ExpandFields(t.Context(), scope, parsedWords(t, source)[1])
		testutil.FailErr(t, source, err)
		if len(fields) == 0 || fields[0].Value[1] != '/' {
			t.Fatalf("%s matched %d fields", source, len(fields))
		}
	}
	_, err := expander.ExpandFields(t.Context(), scope, parsedWords(t, `: c/*`)[1])
	var refused *ExpansionError
	if !errors.As(err, &refused) || refused.Limit != "glob_matches" || refused.Maximum != limits.globMatches {
		t.Fatalf("match past the program budget: err = %v", err)
	}
	fields, err := expander.ExpandFields(t.Context(), scope, parsedWords(t, `: c/x`)[1])
	if err != nil || len(fields) != 1 || fields[0].Value != "c/x" {
		t.Fatalf("literal word after an exhausted glob budget: %v %v", fields, err)
	}
}

func TestGlobVisitBoundaries(t *testing.T) {
	limits := expansionLimitsFixture(t)
	visits := int(limits.visits)
	for _, c := range []struct {
		entries int
		limit   string
	}{{visits, ""}, {visits + 1, "traversal_visits"}} {
		files := memoryFiles{"": nil, "d": namedEntries("e", c.entries)}
		fields, limit := expandAt(t, files, `: d/*.none`, "")
		if limit != c.limit || (c.limit == "" && fields != 1) {
			t.Fatalf("%d entries: fields=%d limit=%q", c.entries, fields, limit)
		}
	}
}

func TestExpansionChargesProgramMemory(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 256, State: 1 << 20})
	testutil.FailErr(t, "small program memory", err)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	scope := testScope(t, memory, []string{"V=" + strings.Repeat("v", 512)})
	_, err = expander.ExpandFields(t.Context(), scope, parsedWords(t, `: a"$V"b`)[1])
	var exhausted *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &exhausted) || exhausted.Account != "working" {
		t.Fatalf("err = %v, want working memory refusal", err)
	}
	if memory.InUse().Working != 0 {
		t.Fatalf("refused expansion retained %+v", memory.InUse())
	}
	expander.live = expander.limits.memory - pieceBytes
	_, err = expander.ExpandFields(t.Context(), scope, parsedWords(t, `: a b`)[1])
	testutil.FailErr(t, "expansion memory at its limit", err)
	expander.live = expander.limits.memory - pieceBytes + 1
	_, err = expander.ExpandFields(t.Context(), scope, parsedWords(t, `: a b`)[1])
	var refused *ExpansionError
	if !errors.As(err, &refused) || refused.Limit != "expansion_memory_bytes" {
		t.Fatalf("err = %v, want expansion memory refusal", err)
	}
}

func TestWordExpanderRequiresItsPorts(t *testing.T) {
	evaluator := &fixtureEvaluator{}
	files := commandsurface.OSGlobFiles{}
	for name, ports := range map[string]ExpanderPorts{
		"substitution": {Files: files, LocalBoundsOnly: true},
		"files":        {Substitutions: evaluator, LocalBoundsOnly: true},
		"memory":       {Substitutions: evaluator, Files: files},
	} {
		if _, err := NewWordExpander(ports); err == nil {
			t.Errorf("expander without %s accepted", name)
		}
	}
	expander, err := NewWordExpander(ExpanderPorts{Substitutions: evaluator, Files: files, LocalBoundsOnly: true})
	testutil.FailErr(t, "fixture bounds expander", err)
	if _, err := expander.ExpandFields(t.Context(), nil, commandsurface.DeferredWord{}); err == nil {
		t.Fatal("expansion without a scope accepted")
	}
	fields, err := expander.ExpandFields(t.Context(), testScope(t, testMemory(t), nil), commandsurface.DeferredWord{})
	if err != nil || len(fields) != 0 {
		t.Fatalf("empty word = %v %v", fields, err)
	}
}
