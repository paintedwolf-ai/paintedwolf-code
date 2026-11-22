package commandmode

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native/textutil/sedscript"
)

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

// SelectSedMode parses an inline sed invocation and returns its catalog effect mode.
func SelectSedMode(ctx context.Context, bound commandprogram.BoundArgv) (string, bool) {
	setup, err := sharedSedSetup()
	if err != nil {
		return "", false
	}
	compiler, limits := setup.compiler, setup.limits
	mem, err := toolcontract.NewProgramMemory(limits)
	if err != nil {
		return "", false
	}
	operands := bound.Operands()
	inPlace := false
	for _, key := range []string{"-i", "-I"} {
		if _, ok := bound.Last(key); ok {
			inPlace = true
		}
	}
	if opt, ok := bound.Last("-i"); ok && len(opt.Values) == 0 && len(operands) > 0 && operands[0].Value == "" &&
		(len(bound.Options("-e")) > 0 || len(bound.Options("-f")) > 0) {
		operands = operands[1:]
	}
	var parts []string
	fromFile := false
	for _, item := range bound.Items {
		opt := item.Option
		if opt == nil || (opt.Key != "-e" && opt.Key != "-f") {
			continue
		}
		val := opt.Values[0].Value
		if opt.Key == "-e" {
			parts = append(parts, val)
			continue
		}
		if val == "-" {
			return "", false
		}
		fromFile = true
	}
	if fromFile {
		if inPlace {
			if len(operands) == 0 {
				return "", false
			}
			return "-i", true
		}
		return "", false
	}
	_, extended := bound.Last("-E")
	var program *sedscript.Program
	files := operands
	if len(parts) > 0 {
		program, err = sedscript.Parse(ctx, mem, strings.Join(parts, "\n"), extended, compiler)
		if err != nil {
			return "", false
		}
	} else {
		if len(operands) == 0 {
			return "", false
		}
		program, files, err = parsePositionalSedMode(ctx, mem, bound, operands, extended, compiler)
		if err != nil {
			return "", false
		}
	}
	defer program.Close()
	if inPlace && len(files) == 0 {
		return "", false
	}
	return SedCompiledMode(inPlace, program.WriteFiles()), true
}

func parsePositionalSedMode(
	ctx context.Context,
	mem *toolcontract.ProgramMemory,
	bound commandprogram.BoundArgv,
	operands commandprogram.Fields,
	extended bool,
	compiler posixSedCompiler,
) (*sedscript.Program, commandprogram.Fields, error) {
	firstProg, firstErr := sedscript.Parse(ctx, mem, operands[0].Value, extended, compiler)
	if firstErr == nil {
		return firstProg, operands[1:], nil
	}
	opt, hasInPlace := bound.Last("-i")
	_, hasE := bound.Last("-e")
	_, hasF := bound.Last("-f")
	_, hasBigI := bound.Last("-I")
	if hasInPlace && len(opt.Values) == 0 && len(operands) >= 2 && !hasE && !hasF && !hasBigI {
		secondProg, secondErr := sedscript.Parse(ctx, mem, operands[1].Value, extended, compiler)
		if secondErr == nil {
			return secondProg, operands[2:], nil
		}
	}
	return nil, nil, firstErr
}
