package commandprogram

import (
	"context"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// parsedStatements parses a real program through the admission parser.
func parsedStatements(t *testing.T, source string) []commandsurface.Statement {
	t.Helper()
	parsed, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{SourceName: "inline"})
	testutil.FailErr(t, "parse "+source, err)
	return parsed.Snapshot().Statements
}

// parsedWords returns the argument words of a program's single simple command.
func parsedWords(t *testing.T, source string) []commandsurface.DeferredWord {
	t.Helper()
	command, ok := parsedStatements(t, source)[0].Command.(commandsurface.SimpleCommand)
	if !ok {
		t.Fatalf("%q is not a simple command", source)
	}
	return command.Words
}

func parsedAssignment(t *testing.T, source string) commandsurface.Assignment {
	t.Helper()
	command, ok := parsedStatements(t, source)[0].Command.(commandsurface.SimpleCommand)
	if !ok || len(command.Assignments) == 0 {
		t.Fatalf("%q has no assignment", source)
	}
	return command.Assignments[0]
}

func testMemory(t *testing.T) *toolcontract.ProgramMemory {
	t.Helper()
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "catalog memory limits", err)
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "program memory", err)
	return memory
}

func testScope(t *testing.T, memory *toolcontract.ProgramMemory, environment []string, positional ...string) *Variables {
	t.Helper()
	scope, err := NewVariables(t.Context(), memory, "inline", environment, positional)
	testutil.FailErr(t, "invocation variables", err)
	t.Cleanup(scope.Close)
	return scope
}

type substitutionCall struct {
	node  commandsurface.NodeID
	depth int
	argv  []string
}

// fixtureEvaluator stands in for the evaluator's Substitute: it expands each
// unit statement through the same expander, as the real evaluator does, and
// runs a table of fixture commands. calls is its receipt log.
type fixtureEvaluator struct {
	expander   *WordExpander
	calls      []substitutionCall
	lastStatus int
}

func completed(status int) toolcontract.ProducerResult {
	return toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: status == 0, EffectCertainty: toolcontract.NoEffect}
}

func (f *fixtureEvaluator) Substitute(ctx context.Context, scope ExpansionScope, request SubstitutionRequest, stdout io.Writer) (toolcontract.ProducerResult, error) {
	result := completed(0)
	for _, statement := range request.Unit.Statements {
		command, ok := statement.Command.(commandsurface.SimpleCommand)
		if !ok {
			return toolcontract.ProducerResult{}, errors.New("fixture evaluates simple commands")
		}
		var argv []string
		for _, word := range command.Words {
			fields, err := f.expander.ExpandFields(ctx, scope, word)
			if err != nil {
				return toolcontract.ProducerResult{}, err
			}
			argv = append(argv, fields.Values()...)
		}
		f.calls = append(f.calls, substitutionCall{node: request.Unit.ID, depth: request.Depth, argv: argv})
		var err error
		if result, err = fixtureCommand(argv, stdout); err != nil {
			return result, err
		}
		f.lastStatus = result.Status
	}
	return result, nil
}

func fixtureCommand(argv []string, stdout io.Writer) (toolcontract.ProducerResult, error) {
	switch argv[0] {
	case "echo":
		_, err := io.WriteString(stdout, strings.Join(argv[1:], " ")+"\n")
		return completed(0), err
	case "printf":
		_, err := io.WriteString(stdout, argv[1])
		return completed(0), err
	case "lines":
		_, err := io.WriteString(stdout, strings.Join(argv[1:], "\n")+"\n\n\n")
		return completed(0), err
	case "bytes":
		n, _ := strconv.Atoi(argv[1])
		_, err := io.WriteString(stdout, strings.Repeat("x", n))
		return completed(0), err
	case "status":
		n, _ := strconv.Atoi(argv[1])
		_, err := io.WriteString(stdout, strings.Join(argv[2:], " ")+"\n")
		return completed(n), err
	case "refuse":
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: "PERMISSION_DENIED", EffectCertainty: toolcontract.NoEffect}, nil
	case "invalid":
		return toolcontract.ProducerResult{Status: 300, Terminal: toolcontract.ProducerCompleted, EffectCertainty: toolcontract.NoEffect}, nil
	case "broken":
		return toolcontract.ProducerResult{}, errors.New("dispatch failed")
	}
	return toolcontract.ProducerResult{}, errors.New("unknown fixture command " + argv[0])
}

// memoryFiles is an in-memory logical tree for budget boundaries.
type memoryFiles map[string][]commandsurface.GlobEntry

func (m memoryFiles) ListDir(_ context.Context, dir string, maximum int) ([]commandsurface.GlobEntry, error) {
	entries := m[dir]
	if len(entries) > maximum+1 {
		entries = entries[:maximum+1]
	}
	return entries, nil
}
func (m memoryFiles) Entry(_ context.Context, path string) (commandsurface.GlobEntry, bool, error) {
	_, dir := m[path]
	return commandsurface.GlobEntry{Dir: dir}, dir, nil
}
func (memoryFiles) Admit(context.Context, string) (bool, error) { return true, nil }

func namedEntries(prefix string, count int) []commandsurface.GlobEntry {
	entries := make([]commandsurface.GlobEntry, count)
	for i := range entries {
		entries[i] = commandsurface.GlobEntry{Name: prefix + strconv.Itoa(i)}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// fixtureExpander builds the expander and its fixture evaluator over files.
func fixtureExpander(t *testing.T, files commandsurface.GlobFiles, memory *toolcontract.ProgramMemory) (*WordExpander, *fixtureEvaluator) {
	t.Helper()
	evaluator := &fixtureEvaluator{}
	expander, err := NewWordExpander(ExpanderPorts{Substitutions: evaluator, Files: files, Memory: memory})
	testutil.FailErr(t, "word expander", err)
	evaluator.expander = expander
	return expander, evaluator
}

func fieldValues(t *testing.T, expander *WordExpander, scope ExpansionScope, words []commandsurface.DeferredWord) []string {
	t.Helper()
	values := []string{}
	for _, word := range words {
		fields, err := expander.ExpandFields(t.Context(), scope, word)
		testutil.FailErr(t, "expand fields", err)
		for _, field := range fields {
			if field.Span != word.Span {
				t.Fatalf("field %q span %+v, want word span %+v", field.Value, field.Span, word.Span)
			}
		}
		values = append(values, fields.Values()...)
	}
	return values
}
