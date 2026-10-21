package commandprogram

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func substitutionParts(word commandsurface.DeferredWord) []commandsurface.SubstitutionPart {
	var out []commandsurface.SubstitutionPart
	var walk func([]commandsurface.WordNode)
	walk = func(parts []commandsurface.WordNode) {
		for _, part := range parts {
			switch part := part.(type) {
			case commandsurface.SubstitutionPart:
				out = append(out, part)
			case commandsurface.DoubleQuotedPart:
				walk(part.Parts)
			}
		}
	}
	walk(word.Parts)
	return out
}

func TestSubstitutionOutputIsDataWithOrdinaryStatus(t *testing.T) {
	cases := []struct {
		source string
		want   []string
		status int
	}{
		{`: $(echo a b)`, []string{"a", "b"}, 0},
		{`: "$(echo a b)"`, []string{"a b"}, 0},
		{`: $(lines a b)`, []string{"a", "b"}, 0},
		{`: "$(lines a b)"`, []string{"a\nb"}, 0},
		{": `echo tick`", []string{"tick"}, 0},
		{`: "$(printf "$CR")"`, []string{"x\r"}, 0},
		{`: "$(printf ';$(rm -rf /)|` + "`id`" + `')"`, []string{";$(rm -rf /)|`id`"}, 0},
		{`: $(status 3 kept)`, []string{"kept"}, 3},
		{`: "$(echo $(echo inner))"`, []string{"inner"}, 0},
		{`: x$(echo)y`, []string{"xy"}, 0},
		{`: $(echo)`, []string{}, 0},
		{`: "$(echo)"`, []string{""}, 0},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			memory := testMemory(t)
			scope := testScope(t, memory, []string{"CR=x\r\n\n"})
			expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
			got := fieldValues(t, expander, scope, parsedWords(t, c.source)[1:])
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%s expanded to %q, want %q", c.source, got, c.want)
			}
			if evaluator.lastStatus != c.status || scope.LastStatus() != 0 {
				t.Fatalf("evaluator status %d (want %d), scope status %d", evaluator.lastStatus, c.status, scope.LastStatus())
			}
		})
	}
}

func TestBinarySubstitutionBytesArePreserved(t *testing.T) {
	memory := testMemory(t)
	scope := testScope(t, memory, nil)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	got, err := expander.ExpandScalar(t.Context(), scope, parsedAssignment(t, "x=$(printf \"$B\")").Value)
	testutil.FailErr(t, "unset binary source", err)
	if got != "" {
		t.Fatalf("unset printf operand produced %q", got)
	}
	binary := testScope(t, memory, []string{"B=\xff\xfe\x01\n"})
	got, err = expander.ExpandScalar(t.Context(), binary, parsedAssignment(t, "x=$(printf \"$B\")").Value)
	testutil.FailErr(t, "binary substitution", err)
	if got != "\xff\xfe\x01" {
		t.Fatalf("binary substitution = %q", got)
	}
}

func TestNestedSubstitutionCarriesDepth(t *testing.T) {
	memory := testMemory(t)
	expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	got := fieldValues(t, expander, testScope(t, memory, nil), parsedWords(t, `: $(echo $(echo $(echo deep)))`)[1:])
	if !reflect.DeepEqual(got, []string{"deep"}) {
		t.Fatalf("nested substitution = %q", got)
	}
	var depths []int
	for _, call := range evaluator.calls {
		depths = append(depths, call.depth)
	}
	if !reflect.DeepEqual(depths, []int{3, 2, 1}) {
		t.Fatalf("receipts recorded depths %v, want innermost first", depths)
	}
	nodes := map[commandsurface.NodeID]bool{}
	for _, call := range evaluator.calls {
		nodes[call.node] = true
	}
	if len(nodes) != 3 {
		t.Fatalf("substitutions share receipts: %+v", evaluator.calls)
	}
}

func TestFailedSubstitutionStopsTheWordAndKeepsEarlierReceipts(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		terminal toolcontract.ProducerTerminal
		cause    bool
	}{
		{"host refusal", `: $(echo first)$(refuse)$(echo never)`, toolcontract.ProducerRefused, false},
		{"dispatch error", `: "$(echo first)$(broken)$(echo never)"`, "", true},
		{"invalid producer result", `: $(echo first)$(invalid)$(echo never)`, toolcontract.ProducerCompleted, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			memory := testMemory(t)
			expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
			word := parsedWords(t, c.source)[1]
			_, err := expander.ExpandFields(t.Context(), testScope(t, memory, nil), word)
			var failed *SubstitutionError
			if !errors.As(err, &failed) {
				t.Fatalf("err = %v, want a substitution failure", err)
			}
			failing := substitutionParts(word)[1]
			if failed.Span != failing.Span || failed.Node != failing.ID || failed.Result.Terminal != c.terminal || (failed.Cause != nil) != c.cause {
				t.Fatalf("failure %+v, want span %+v of %s", failed, failing.Span, failing.ID)
			}
			if len(evaluator.calls) != 2 || evaluator.calls[0].argv[1] != "first" {
				t.Fatalf("receipts %+v, want the earlier and the failing unit only", evaluator.calls)
			}
			if memory.InUse().Working != 0 {
				t.Fatalf("failed expansion retained memory %+v", memory.InUse())
			}
		})
	}
}

func TestNulValuesAreRefusedAtTheirSpan(t *testing.T) {
	memory := testMemory(t)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	scope := testScope(t, memory, nil)
	scope.values["NUL"] = variable{value: "a\x00b", set: true, release: func() {}}
	word := parsedWords(t, `: ok"$NUL"`)[1]
	_, err := expander.ExpandFields(t.Context(), scope, word)
	var refused *ExpansionError
	if !errors.As(err, &refused) || refused.Code != codeOperandInvalid || refused.Span != word.Span {
		t.Fatalf("err = %v, want NUL refusal at %+v", err, word.Span)
	}
	expander.substitutions = substitutionFunc(func(SubstitutionRequest) ([]byte, toolcontract.ProducerResult) {
		return []byte("a\x00b\n"), completed(0)
	})
	word = parsedWords(t, `: ok$(nul)`)[1]
	_, err = expander.ExpandFields(t.Context(), scope, word)
	if !errors.As(err, &refused) || refused.Code != codeOperandInvalid || refused.Span != substitutionParts(word)[0].Span {
		t.Fatalf("err = %v, want NUL refusal at the substitution span", err)
	}
}

// substitutionFunc writes fixed output for each unit.
type substitutionFunc func(SubstitutionRequest) ([]byte, toolcontract.ProducerResult)

func (f substitutionFunc) Substitute(_ context.Context, _ ExpansionScope, request SubstitutionRequest, stdout io.Writer) (toolcontract.ProducerResult, error) {
	output, result := f(request)
	_, err := stdout.Write(output)
	return result, err
}

func TestSubstitutionBoundaries(t *testing.T) {
	memory := testMemory(t)
	limits, err := catalogExpansionLimits()
	testutil.FailErr(t, "limits", err)
	expander, _ := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	scope := testScope(t, memory, nil)
	for _, c := range []struct {
		size  int64
		allow bool
	}{{limits.substitutionBytes, true}, {limits.substitutionBytes + 1, false}} {
		value := parsedAssignment(t, "x=$(bytes "+strconv.FormatInt(c.size, 10)+")").Value
		got, err := expander.ExpandScalar(t.Context(), scope, value)
		var refused *ExpansionError
		switch {
		case c.allow && (err != nil || int64(len(got)) != c.size):
			t.Fatalf("%d substitution bytes refused: %v", c.size, err)
		case !c.allow && (!errors.As(err, &refused) || refused.Limit != "substitution_bytes" || refused.Span != substitutionParts(value)[0].Span):
			t.Fatalf("%d substitution bytes: err = %v", c.size, err)
		}
	}
	nested := func(levels int) string {
		return ": " + strings.Repeat("$(echo ", levels) + "x" + strings.Repeat(")", levels)
	}
	got := fieldValues(t, expander, scope, parsedWords(t, nested(int(limits.substitutionDepth)))[1:])
	if !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("depth at limit = %q", got)
	}
	_, err = expander.ExpandFields(t.Context(), scope, parsedWords(t, nested(int(limits.substitutionDepth)+1))[1])
	var refused *ExpansionError
	if !errors.As(err, &refused) || refused.Limit != "substitution_depth" || refused.Observed != limits.substitutionDepth+1 {
		t.Fatalf("depth over limit: err = %v", err)
	}
}

func TestCanceledExpansionStopsBeforeDispatch(t *testing.T) {
	memory := testMemory(t)
	expander, evaluator := fixtureExpander(t, commandsurface.OSGlobFiles{Dir: t.TempDir()}, memory)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := expander.ExpandFields(ctx, testScope(t, memory, nil), parsedWords(t, `: $(echo x)`)[1])
	if !errors.Is(err, context.Canceled) || len(evaluator.calls) != 0 {
		t.Fatalf("canceled expansion err=%v calls=%v", err, evaluator.calls)
	}
}
