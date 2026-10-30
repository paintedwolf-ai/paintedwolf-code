package commandprogram

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestVariablesExportUnsetAndDispatchEnvironment(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, []string{"PATH=/bin", "HOME=/home/u", "malformed", "=x", "SECRET=" + secretToken})
	ctx := t.Context()
	testutil.FailErr(t, "assign local", scope.Assign(ctx, "LOCAL", "one"))
	testutil.FailErr(t, "export unset", scope.Export(ctx, "LATER"))
	if _, set := scope.Variable("LATER"); set {
		t.Fatal("export created a value")
	}
	testutil.FailErr(t, "assign exported", scope.Assign(ctx, "LATER", "two"))
	testutil.FailErr(t, "reassign exported", scope.Assign(ctx, "HOME", "/home/v"))
	scope.Unset("PATH")
	env, err := scope.Environment(map[string]string{"ONCE": "3", "LATER": "override"})
	testutil.FailErr(t, "dispatch environment", err)
	want := []string{"HOME=/home/v", "LATER=override", "ONCE=3", "SECRET=" + secretToken}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("environment %q, want %q", env, want)
	}
	if value, _ := scope.Variable("LATER"); value != "two" {
		t.Fatalf("dispatch override changed the frame: %q", value)
	}
	if _, set := scope.Variable("ONCE"); set {
		t.Fatal("dispatch prefix leaked into the frame")
	}
	if _, set := scope.Variable("PATH"); set {
		t.Fatal("unset variable still expands")
	}
	memory2 := testMemory(t)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory2)
	got := fieldValues(t, expander, scope, parsedWords(t, `: "$PATH" $LOCAL $LATER "$SECRET"`)[1:])
	if !reflect.DeepEqual(got, []string{"", "one", "two", secretToken}) {
		t.Fatalf("expansion after export/unset = %q", got)
	}
}

func TestVariablesCloneIsolatesFrames(t *testing.T) {
	memory := testMemory(t)
	parent := testScope(t, memory, []string{"A=1"}, "p1")
	parent.SetStatus(4)
	parent.SetBackground("%2")
	child, err := parent.Clone(t.Context())
	testutil.FailErr(t, "clone", err)
	defer child.Close()
	testutil.FailErr(t, "child assign", child.Assign(t.Context(), "A", "2"))
	child.SetPositional([]string{"c1", "c2"})
	child.SetStatus(9)
	if value, _ := parent.Variable("A"); value != "1" || len(parent.Positional()) != 1 || parent.LastStatus() != 4 {
		t.Fatalf("child frame leaked into parent: A=%q positional=%q status=%d", value, parent.Positional(), parent.LastStatus())
	}
	if job, set := child.LastBackground(); !set || job != "%2" || child.ProgramName() != "inline" {
		t.Fatalf("clone lost frame facts: job=%q set=%v program=%q", job, set, child.ProgramName())
	}
	positional := child.Positional()
	positional[0] = "mutated"
	if child.Positional()[0] != "c1" {
		t.Fatal("positional parameters share caller storage")
	}
}

func TestVariablesRefuseInvalidAndOversizedState(t *testing.T) {
	limits := expansionLimitsFixture(t)
	memory := testMemory(t)
	scope := testScope(t, memory, nil)
	ctx := t.Context()
	for name, err := range map[string]error{
		"name":     scope.Assign(ctx, "1x", "v"),
		"ifs":      scope.Assign(ctx, "IFS", ":"),
		"nul":      scope.Assign(ctx, "V", "a\x00b"),
		"export":   scope.Export(ctx, "bad-name"),
		"override": func() error { _, err := scope.Environment(map[string]string{"IFS": " "}); return err }(),
	} {
		var refused *ExpansionError
		if !errors.As(err, &refused) || refused.Code == codeLimitExceeded {
			t.Errorf("%s: err = %v, want a typed refusal", name, err)
		}
	}
	testutil.FailErr(t, "value at limit", scope.Assign(ctx, "V", strings.Repeat("v", int(limits.valueBytes))))
	var refused *ExpansionError
	if err := scope.Assign(ctx, "V", strings.Repeat("v", int(limits.valueBytes)+1)); !errors.As(err, &refused) || refused.Limit != "variable_value_bytes" {
		t.Fatalf("value over limit: %v", err)
	}
	scope.Unset("V")
	for i := 0; i < int(limits.maximum["variable_count"]); i++ {
		testutil.FailErr(t, "variable within count", scope.Assign(ctx, "V"+strconv.Itoa(i), ""))
	}
	if err := scope.Assign(ctx, "ONE_MORE", ""); !errors.As(err, &refused) || refused.Limit != "variable_count" {
		t.Fatalf("variable past count: %v", err)
	}
	testutil.FailErr(t, "replacing within count", scope.Assign(ctx, "V0", "replaced"))
	big := strings.Repeat("e", int(limits.maximum["environment_bytes"])/2)
	envScope := testScope(t, memory, []string{"A=" + big})
	if _, err := envScope.Environment(map[string]string{"B": big[:len(big)-4]}); err != nil {
		t.Fatalf("environment at limit refused: %v", err)
	}
	if _, err := envScope.Environment(map[string]string{"B": big[:len(big)-3]}); !errors.As(err, &refused) || refused.Limit != "environment_bytes" {
		t.Fatalf("environment over limit: %v", err)
	}
}

func TestVariablesChargeProgramState(t *testing.T) {
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 2*(variableBytes+8) - 1})
	testutil.FailErr(t, "small state memory", err)
	scope, err := NewVariables(t.Context(), memory, "inline", []string{"AAA=aaaa"}, nil)
	testutil.FailErr(t, "variables within state", err)
	if got := memory.InUse().State; got != variableBytes+7 {
		t.Fatalf("state charge %d", got)
	}
	testutil.FailErr(t, "replace within state", scope.Assign(t.Context(), "AAA", "bbbbb"))
	var exhausted *toolcontract.ProgramMemoryLimitError
	if err := scope.Assign(t.Context(), "BBB", strings.Repeat("c", 16)); !errors.As(err, &exhausted) {
		t.Fatalf("state past its limit: %v", err)
	}
	clone, err := scope.Clone(t.Context())
	if !errors.As(err, &exhausted) || clone != nil {
		t.Fatalf("clone past the state limit: %v", err)
	}
	scope.Close()
	if memory.InUse().State != 0 {
		t.Fatalf("closed frame retained %+v", memory.InUse())
	}
	if _, err := NewVariables(t.Context(), nil, "inline", nil, nil); err == nil {
		t.Fatal("variables without state admission accepted")
	}
	if _, err := NewVariables(t.Context(), memory, "inline", []string{"A=" + strings.Repeat("a", 512)}, nil); !errors.As(err, &exhausted) {
		t.Fatalf("environment past the state limit: %v", err)
	}
}
