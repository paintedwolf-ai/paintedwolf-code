package conformance_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type noopSubstitution struct{}

func (noopSubstitution) Substitute(_ context.Context, _ commandprogram.ExpansionScope, req commandprogram.SubstitutionRequest, stdout io.Writer) (toolcontract.ProducerResult, error) {
	_, _ = io.WriteString(stdout, "sub:"+string(req.Unit.ID)+"\n")
	return toolcontract.ProducerResult{
		Status:              0,
		Terminal:            toolcontract.ProducerCompleted,
		Complete:            true,
		PublicationEligible: true,
		EffectCertainty:     toolcontract.NoEffect,
	}, nil
}

func checkParseInvariants(t *testing.T, raw []byte, sourced bool, limits toolcontract.ProgramMemoryLimits) {
	t.Helper()
	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "create program memory", err)

	parsed, err := commandsurface.ParseAdmitted(t.Context(), memory, raw, commandsurface.ParseOptions{
		SourceName: "fuzz",
		Sourced:    sourced,
	})
	if err != nil {
		var syntaxDiag *commandsurface.SyntaxDiagnostic
		var memErr *toolcontract.ProgramMemoryLimitError
		if !errors.As(err, &syntaxDiag) && !errors.As(err, &memErr) {
			t.Fatalf("unexpected untyped parse error %T (%v) for input %q", err, err, string(raw))
		}
		if used := memory.InUse(); used.Working != 0 || used.State != 0 {
			t.Fatalf("parse failure leaked program memory %+v for input %q", used, string(raw))
		}
		return
	}

	snap, snapErr := parsed.SnapshotWithMemory(t.Context(), memory)
	if snapErr != nil {
		var memErr *toolcontract.ProgramMemoryLimitError
		if !errors.As(snapErr, &memErr) {
			t.Fatalf("unexpected snapshot error %T (%v)", snapErr, snapErr)
		}
		testutil.FailErr(t, "close parsed program after snapshot refusal", parsed.Close())
		if used := memory.InUse(); used.Working != 0 || used.State != 0 {
			t.Fatalf("snapshot refusal leaked memory %+v", used)
		}
		return
	}
	testutil.FailErr(t, "close snapshot", snap.Close())
	testutil.FailErr(t, "close parsed program", parsed.Close())
	if used := memory.InUse(); used.Working != 0 || used.State != 0 {
		t.Fatalf("closed parsed program leaked memory %+v", used)
	}
}

func FuzzParseProgramSafetyAndCeilings(f *testing.F) {
	seeds := []string{
		"printf '%s\\n' hello",
		"cat notes.txt | tr 'a-z' 'A-Z' | head -n 2",
		"for x in a b c; do printf '%s\\n' \"$x\"; done",
		"if [ -f notes.txt ] && [[ \"abc\" =~ ^a.*c$ ]]; then echo ok; fi",
		"fn() { return 3; }; fn || echo $?",
		"cat <<'EOF'\nliteral $x\nEOF\n",
		"echo 'unterminated single quote",
		"echo \"unterminated double quote",
		"cat <<EOF\nunterminated heredoc",
		"a |& b",
		"select x in a b; do echo $x; done",
		"until false; do echo x; done",
		"coproc echo hi",
		"cat <(echo proc_sub)",
		"echo {a,b,c}",
		"echo $((1 / 0))",
		strings.Repeat("( ", 40) + "true" + strings.Repeat(" )", 40),
		"printf 'a\x00b'",
	}
	for _, s := range seeds {
		f.Add([]byte(s), false)
	}

	baseLimits, err := toolcontract.CatalogProgramMemoryLimits()
	if err != nil {
		f.Fatalf("load catalog memory limits: %v", err)
	}

	f.Fuzz(func(t *testing.T, raw []byte, sourced bool) {
		checkParseInvariants(t, raw, sourced, baseLimits)
		tight := baseLimits
		tight.Working = 2048
		tight.State = 2048
		checkParseInvariants(t, raw, sourced, tight)
	})
}

func checkWordExpansionInvariants(t *testing.T, expr string, globRoot string, limits toolcontract.ProgramMemoryLimits) {
	t.Helper()
	source := ": " + expr
	parsed, err := commandsurface.ParseProgram([]byte(source), commandsurface.ParseOptions{SourceName: "fuzz-exp"})
	if err != nil {
		return
	}
	defer func() { _ = parsed.Close() }()
	stmts := parsed.Snapshot().Statements
	if len(stmts) != 1 {
		return
	}
	simple, ok := stmts[0].Command.(commandsurface.SimpleCommand)
	if !ok || len(simple.Words) < 2 {
		return
	}

	memory, err := toolcontract.NewProgramMemory(limits)
	testutil.FailErr(t, "new program memory", err)
	scope, err := commandprogram.NewVariables(t.Context(), memory, "fuzz", []string{"HOME=/tmp", "A=alpha beta", "NUM=42", "EMPTY="}, []string{"pos1", "pos2"})
	if err != nil {
		return
	}
	defer scope.Close()

	expander, err := commandprogram.NewWordExpander(commandprogram.ExpanderPorts{
		Substitutions: noopSubstitution{},
		Files:         commandsurface.OSGlobFiles{Dir: globRoot},
		Memory:        memory,
	})
	testutil.FailErr(t, "new word expander", err)

	for _, word := range simple.Words[1:] {
		if _, err := expander.ExpandFields(t.Context(), scope, word); err != nil {
			assertTypedExpansionError(t, err)
		}
		if _, err := expander.ExpandScalar(t.Context(), scope, word); err != nil {
			assertTypedExpansionError(t, err)
		}
		if _, err := expander.ExpandPattern(t.Context(), scope, word); err != nil {
			assertTypedExpansionError(t, err)
		}
	}
	if used := memory.InUse(); used.Working != 0 {
		t.Fatalf("word expansion leaked working memory %+v on %q", used, expr)
	}
}

func assertTypedExpansionError(t *testing.T, err error) {
	t.Helper()
	var expErr *commandprogram.ExpansionError
	var memErr *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &expErr) && !errors.As(err, &memErr) {
		t.Fatalf("unexpected untyped expansion error %T: %v", err, err)
	}
}

func FuzzWordExpanderSafetyAndCeilings(f *testing.F) {
	seeds := []string{
		`$A "$A" '${A}'`,
		`${A:-fallback} ${EMPTY:+alt} ${#A} ${A#alpha } ${A%beta}`,
		`$((1 + 2 * 3 - (4 / 2)))`,
		`$((1 / 0))`,
		`$((1 << 100))`,
		`$(printf 'nested')`,
		`**/*.txt *[a-z]* ?`,
		`$@ "$@" $* "$*" $# $? $! $0 $1`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	baseLimits, err := toolcontract.CatalogProgramMemoryLimits()
	if err != nil {
		f.Fatalf("load catalog memory limits: %v", err)
	}
	globRoot := f.TempDir()
	testutil.FailErr(f, "seed glob file", os.WriteFile(filepath.Join(globRoot, "a.txt"), []byte("hi\n"), 0o644))

	f.Fuzz(func(t *testing.T, expr string) {
		checkWordExpansionInvariants(t, expr, globRoot, baseLimits)
	})
}

func FuzzCatalogBinderSafetyAndTypedRefusals(f *testing.F) {
	seeds := []struct {
		cmd  string
		arg1 string
		arg2 string
	}{
		{"grep", "-n", "alpha"},
		{"grep", "--not-an-option", "alpha"},
		{"head", "-n", "5"},
		{"head", "-5", "notes.txt"},
		{"sed", "-i", "s/a/b/"},
		{"git", "status", "-s"},
		{"git", "-C", "."},
		{"echo", "-n", "-z"},
	}
	for _, s := range seeds {
		f.Add(s.cmd, s.arg1, s.arg2)
	}

	binder := commandprogram.CatalogBinder{}

	f.Fuzz(func(t *testing.T, cmd, arg1, arg2 string) {
		fields := commandprogram.Fields{
			{Value: cmd},
			{Value: arg1},
			{Value: arg2},
		}
		spec, ok, err := commandprogram.ResolveCommand(fields)
		if err != nil || !ok {
			return
		}
		_, bindErr := binder.Bind(spec, fields)
		if bindErr != nil {
			var argvErr *commandprogram.ArgvError
			if !errors.As(bindErr, &argvErr) {
				t.Fatalf("unexpected untyped binder error %T: %v", bindErr, bindErr)
			}
		}
	})
}

func TestAdversarialParserAndExpansionCeilings(t *testing.T) {
	baseLimits, err := toolcontract.CatalogProgramMemoryLimits()
	testutil.FailErr(t, "load catalog memory limits", err)

	adversarialPrograms := []string{
		"echo 'unclosed",
		"cat <<EOF\nunterminated",
		"printf 'null\x00byte'",
		"a |& b",
		"select x in 1 2; do echo $x; done",
		"until true; do :; done",
		"coproc true",
		"cat <(true)",
		"echo {1..10}",
		strings.Repeat("if true; then ", 80) + "true" + strings.Repeat("; fi", 80),
	}
	for _, prog := range adversarialPrograms {
		checkParseInvariants(t, []byte(prog), false, baseLimits)
		tight := baseLimits
		tight.Working = 512
		tight.State = 512
		checkParseInvariants(t, []byte(prog), false, tight)
	}

	globRoot := t.TempDir()
	testutil.FailErr(t, "seed glob file", os.WriteFile(filepath.Join(globRoot, "sample.txt"), []byte("ok\n"), 0o644))
	for _, expr := range []string{
		`$((1 / 0))`,
		`$((1 % 0))`,
		`$((999999999999999999999999999999))`,
		`${A:-default} ${#A} ${A#a} ${A##*a} ${A%a} ${A%%a*}`,
		`**/*.txt [a-z]*`,
	} {
		checkWordExpansionInvariants(t, expr, globRoot, baseLimits)
	}
}
