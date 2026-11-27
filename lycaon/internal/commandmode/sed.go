package commandmode

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/textutil/sedscript"
)

// SedSource describes how sed arguments supply script text, in-place backup, and file operands.
type SedSource struct {
	Text     string
	Word     commandprogram.Field
	Files    commandprogram.Fields
	FromFile bool
	InPlace  bool
	Suffix   string
}

// PreparedSed holds parsed sed arguments and any compiled inline program.
type PreparedSed struct {
	Source  SedSource
	Program *sedscript.Program
}

// SedFailure reports the offending argument field and usage or script error when sed preparation fails.
type SedFailure struct {
	Word  commandprogram.Field
	Usage string
	Err   error
}

// SedCompiledMode returns the catalog effect mode for a compiled sed script.
func SedCompiledMode(inPlace bool, writeFiles []string) string {
	if inPlace {
		return "-i"
	}
	if len(writeFiles) > 0 {
		return "w-command"
	}
	return "stream-script"
}

// PrepareSedArguments interprets sed options, positional backup suffixes, file operands,
// and inline scripts. Forms with -f defer file reading and return a nil Program.
func PrepareSedArguments(
	ctx context.Context,
	bound commandprogram.BoundArgv,
	commandField commandprogram.Field,
	mem *toolcontract.ProgramMemory,
	compiler sedscript.Compiler,
) (PreparedSed, *SedFailure) {
	operands := bound.Operands()
	var out SedSource
	for _, key := range []string{"-i", "-I"} {
		if option, ok := bound.Last(key); ok {
			out.InPlace = true
			if len(option.Values) > 0 {
				out.Suffix = option.Values[0].Value
			}
		}
	}
	if option, ok := bound.Last("-i"); ok && len(option.Values) == 0 && len(operands) > 0 && operands[0].Value == "" &&
		(len(bound.Options("-e")) > 0 || len(bound.Options("-f")) > 0) {
		operands = operands[1:]
	}
	var parts []string
	for _, item := range bound.Items {
		option := item.Option
		if option == nil || (option.Key != "-e" && option.Key != "-f") {
			continue
		}
		value := option.Values[0]
		if len(parts) == 0 && !out.FromFile {
			out.Word = value
		}
		if option.Key == "-e" {
			parts = append(parts, value.Value)
			continue
		}
		if value.Value == "-" {
			return PreparedSed{}, &SedFailure{Word: value, Usage: "a script file; standard input is the data"}
		}
		out.FromFile = true
	}
	if out.FromFile {
		out.Files = operands
		if out.InPlace && len(out.Files) == 0 {
			return PreparedSed{}, &SedFailure{Word: commandField, Usage: "a PATH to edit in place"}
		}
		return PreparedSed{Source: out}, nil
	}
	_, extended := bound.Last("-E")
	var program *sedscript.Program
	if len(parts) > 0 {
		out.Text, out.Files = strings.Join(parts, "\n"), operands
		var err error
		program, err = sedscript.Parse(ctx, mem, out.Text, extended, compiler)
		if err != nil {
			return PreparedSed{}, &SedFailure{Word: out.Word, Err: err}
		}
	} else {
		if len(operands) == 0 {
			return PreparedSed{}, &SedFailure{Word: commandField, Usage: "a SCRIPT"}
		}
		var fail *SedFailure
		program, fail = parsePositionalSed(ctx, mem, bound, operands, extended, compiler, &out)
		if fail != nil {
			return PreparedSed{}, fail
		}
	}
	if out.InPlace && len(out.Files) == 0 {
		program.Close()
		return PreparedSed{}, &SedFailure{Word: commandField, Usage: "a PATH to edit in place"}
	}
	return PreparedSed{Source: out, Program: program}, nil
}

func parsePositionalSed(
	ctx context.Context,
	mem *toolcontract.ProgramMemory,
	bound commandprogram.BoundArgv,
	operands commandprogram.Fields,
	extended bool,
	compiler sedscript.Compiler,
	out *SedSource,
) (*sedscript.Program, *SedFailure) {
	firstProg, firstErr := sedscript.Parse(ctx, mem, operands[0].Value, extended, compiler)
	if firstErr == nil {
		out.Text, out.Word, out.Files = operands[0].Value, operands[0], operands[1:]
		return firstProg, nil
	}
	if isSedResourceOrContextError(firstErr) {
		return nil, &SedFailure{Word: operands[0], Err: firstErr}
	}
	option, hasInPlace := bound.Last("-i")
	_, hasE := bound.Last("-e")
	_, hasF := bound.Last("-f")
	_, hasBigI := bound.Last("-I")
	if hasInPlace && len(option.Values) == 0 && len(operands) >= 2 && !hasE && !hasF && !hasBigI {
		secondProg, secondErr := sedscript.Parse(ctx, mem, operands[1].Value, extended, compiler)
		if secondErr == nil {
			out.Suffix = operands[0].Value
			out.Text, out.Word, out.Files = operands[1].Value, operands[1], operands[2:]
			return secondProg, nil
		}
		if isSedResourceOrContextError(secondErr) {
			return nil, &SedFailure{Word: operands[1], Err: secondErr}
		}
	}
	return nil, &SedFailure{Word: operands[0], Err: firstErr}
}

func isSedResourceOrContextError(err error) bool {
	if err == nil {
		return false
	}
	var memErr *toolcontract.ProgramMemoryLimitError
	var budgetErr *textsearch.BudgetError
	var limitErr *sedscript.LimitError
	return errors.As(err, &memErr) || errors.As(err, &budgetErr) || errors.As(err, &limitErr) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type posixSedCompiler struct {
	engine textsearch.POSIXEngine
}

func (c posixSedCompiler) Compile(ctx context.Context, pattern string, extended, fold bool) (sedscript.Regex, error) {
	if fold {
		return c.engine.CompileFolded(ctx, pattern, extended)
	}
	return c.engine.Compile(ctx, pattern, extended)
}

type sedSetupState struct {
	compiler posixSedCompiler
	limits   toolcontract.ProgramMemoryLimits
}

var sharedSedSetup = sync.OnceValues(func() (sedSetupState, error) {
	engine, err := textsearch.NewPOSIXEngine()
	if err != nil {
		return sedSetupState{}, err
	}
	limits, err := toolcontract.CatalogProgramMemoryLimits()
	if err != nil {
		return sedSetupState{}, err
	}
	return sedSetupState{compiler: posixSedCompiler{engine: engine}, limits: limits}, nil
})

// SelectSedMode parses a sed invocation and returns its catalog effect mode.
// File-backed scripts (-f) remain unknown unless -i already fixes in-place mode.
func SelectSedMode(ctx context.Context, bound commandprogram.BoundArgv) (string, bool) {
	setup, err := sharedSedSetup()
	if err != nil {
		return "", false
	}
	mem, err := toolcontract.NewProgramMemory(setup.limits)
	if err != nil {
		return "", false
	}
	prepared, fail := PrepareSedArguments(ctx, bound, commandprogram.Field{Value: "sed"}, mem, setup.compiler)
	if fail != nil {
		return "", false
	}
	if prepared.Program != nil {
		defer prepared.Program.Close()
	}
	if prepared.Source.FromFile {
		if prepared.Source.InPlace {
			return "-i", true
		}
		return "", false
	}
	return SedCompiledMode(prepared.Source.InPlace, prepared.Program.WriteFiles()), true
}
