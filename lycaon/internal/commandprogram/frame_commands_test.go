package commandprogram

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func frameSpec(t *testing.T, name string) toolcontract.CommandSpecification {
	t.Helper()
	catalog, err := toolcontract.LanguageV1()
	testutil.FailErr(t, "load command language catalog", err)
	for _, command := range catalog.Commands {
		if command.Name == name {
			return command
		}
	}
	t.Fatalf("catalog has no %s", name)
	return toolcontract.CommandSpecification{}
}

// frameArgv expands a real parsed command into its argv fields: export
// declarations contribute NAME=VALUE for assignments, then naked words.
func frameArgv(t *testing.T, scope *Variables, source string) Fields {
	t.Helper()
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, testMemory(t))
	var argv Fields
	words := func(items []commandsurface.DeferredWord) {
		for _, word := range items {
			fields, err := expander.ExpandFields(t.Context(), scope, word)
			testutil.FailErr(t, "expand frame argument fields", err)
			argv = append(argv, fields...)
		}
	}
	switch command := parsedStatements(t, source)[0].Command.(type) {
	case commandsurface.SimpleCommand:
		words(command.Words)
	case commandsurface.EnvironmentDeclaration:
		argv = append(argv, Field{Value: "export", Span: command.Span})
		for _, assignment := range command.Assignments {
			value, err := expander.ExpandScalar(t.Context(), scope, assignment.Value)
			testutil.FailErr(t, "expand frame assignment", err)
			argv = append(argv, Field{Value: assignment.Name + "=" + value, Span: assignment.Span})
		}
		words(command.Words)
	}
	return argv
}

func TestFrameCommandsExportAndUnset(t *testing.T) {
	memory := testMemory(t)
	frame := testScope(t, memory, []string{"KEEP=1", "DROP=2"})
	run := func(name, source string) (toolcontract.ProducerResult, error) {
		return FrameCommand(t.Context(), frame, frameSpec(t, name), frameArgv(t, frame, source))
	}
	if err := frame.Assign(t.Context(), "LOCAL", "x"); err != nil {
		testutil.FailErr(t, "assign frame variable", err)
	}
	for _, step := range []struct{ name, source string }{
		{"export", "export LOCAL 'NEW=a b' LATER"},
		{"unset", "unset DROP NEVER"},
		{"export", "export"},
		{"unset", "unset"},
	} {
		result, err := run(step.name, step.source)
		if err != nil || result.Status != 0 || !result.PublicationEligible || result.Validate() != nil {
			t.Fatalf("%s: %+v %v", step.source, result, err)
		}
	}
	env, err := frame.Environment(nil)
	testutil.FailErr(t, "compose frame environment", err)
	if want := []string{"KEEP=1", "LOCAL=x", "NEW=a b"}; !reflect.DeepEqual(env, want) {
		t.Fatalf("environment %q, want %q", env, want)
	}
	if _, set := frame.Variable("LATER"); set {
		t.Fatal("export of an unset name created a value")
	}
}

func TestFrameCommandsRefuseBeforeChangingTheFrame(t *testing.T) {
	memory := testMemory(t)
	frame := testScope(t, memory, []string{"A=1"})
	for _, c := range []struct {
		name, source, code string
		operand            int
	}{
		{"export", `export B=2 "1x=3"`, codeOperandInvalid, 2},
		{"unset", "unset A IFS", codeSyntaxUnsupported, 2},
		{"unset", "unset A=1", codeOperandInvalid, 1},
		{"export", "export -p", argvOptionUnsupported, -1},
	} {
		argv := frameArgv(t, frame, c.source)
		result, err := FrameCommand(t.Context(), frame, frameSpec(t, c.name), argv)
		if result.Terminal != toolcontract.ProducerRefused || result.StopCode != c.code || err == nil {
			t.Fatalf("%s: %+v %v", c.source, result, err)
		}
		var refused *ExpansionError
		if c.operand >= 0 && (!errors.As(err, &refused) || refused.Span != argv[c.operand].Span) {
			t.Fatalf("%s refused at %v, want operand %d", c.source, err, c.operand)
		}
	}
	if value, set := frame.Variable("A"); !set || value != "1" {
		t.Fatal("a refused command changed the frame")
	}
	if _, set := frame.Variable("B"); set {
		t.Fatal("a refused export applied an earlier operand")
	}
	if _, err := FrameCommand(t.Context(), frame, frameSpec(t, "env"), frameArgv(t, frame, "env")); err == nil {
		t.Fatal("env ran as a frame command")
	}
}
