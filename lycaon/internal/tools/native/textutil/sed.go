package textutil

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"github.com/lycaon/lycaon/internal/tools/native/textutil/sedscript"
)

// PosixRegexEngine compiles sed's regular expressions. textsearch.POSIXEngine,
// the shared POSIX BRE/ERE engine, is the one implementation; sed never runs
// on RE2.
type PosixRegexEngine interface {
	Compile(ctx context.Context, pattern string, extended bool) (textsearch.PosixRegex, error)
	CompileFolded(ctx context.Context, pattern string, extended bool) (textsearch.PosixRegex, error)
}

type sedCompiler struct{ engine PosixRegexEngine }

type preparedSed struct {
	source       sedSource
	program      *sedscript.Program
	pins         []toolcontract.ExecutionRevision
	pinsRecorded bool
}

func (c sedCompiler) Compile(ctx context.Context, pattern string, extended, fold bool) (sedscript.Regex, error) {
	if fold {
		return c.engine.CompileFolded(ctx, pattern, extended)
	}
	return c.engine.Compile(ctx, pattern, extended)
}

func (p *preparedSed) close() {
	if p == nil || p.program == nil {
		return
	}
	p.program.Close()
	p.program = nil
}

// runSed executes the prepared sed plan, capturing and admitting any -f script
// files after the child receipt exists and before any input or write is opened,
// opens every w file, then opens read files only when reached and runs the
// inputs as one stream, one stream per file with -s, or edits each file in
// place with -i. Output preserves a final record's missing newline; q and Q set
// the exit status.
func runSed(inv *invocation) error {
	if inv.owners.Regex == nil {
		return toolrejection.RejectInvalidArguments("COMMAND_PLATFORM_UNAVAILABLE", map[string]any{"command": "sed", "capability": "the shared POSIX regex engine"})
	}
	plan := inv.sed
	if plan == nil {
		return errors.New("sed invocation is not prepared")
	}
	script := plan.source
	program := plan.program
	if !plan.pinsRecorded {
		for _, pin := range plan.pins {
			if err := inv.owners.Pins.AppendRevisionPin(inv.ctx, inv.request.Invocation.Identity.ExecutionID, pin); err != nil {
				return err
			}
		}
	}
	if program == nil {
		var err error
		script, program, err = loadSedFileScript(inv, script)
		if err != nil {
			return err
		}
		plan.program = program
	}
	if script.fromFile && len(program.WriteFiles()) > 0 && len(plan.pins) == 0 {
		return unsupported(inv, script.word, "w files named in an inline script (-e or SCRIPT), so the write is reviewed with the command")
	}
	run := &sedRun{inv: inv, program: program, word: script.word, stages: map[string]commandprogram.StagedOutput{}}
	err := run.execute(script)
	if settled := run.settleWrites(err); settled != nil {
		return settled
	}
	if status := run.machine.Status(); status != 0 {
		inv.status = status
	}
	return nil
}

type sedRun struct {
	inv     *invocation
	program *sedscript.Program
	word    commandprogram.Field
	machine *sedscript.Machine
	stages  map[string]commandprogram.StagedOutput
}

func (r *sedRun) execute(script sedSource) error {
	files := script.files
	inv := r.inv
	config, err := r.config()
	if err != nil {
		return err
	}
	if r.machine, err = sedscript.NewMachine(inv.ctx, r.program, config); err != nil {
		return err
	}
	if script.inPlace {
		return r.inPlace(files, script.suffix)
	}
	sources, err := inv.openInputs(files)
	if err != nil {
		return err
	}
	defer closeSources(sources)
	if inv.status != 0 {
		inv.status = 2
	}
	if !inv.flag("-s") {
		input := r.input(sources)
		_, err := r.machine.Stream(input, nil)
		input.Close()
		return r.failure(err)
	}
	for _, input := range sources {
		stream := r.input([]source{input})
		quit, err := r.machine.Stream(stream, nil)
		stream.Close()
		if err != nil || quit {
			return r.failure(err)
		}
	}
	return nil
}

func (r *sedRun) config() (sedscript.Config, error) {
	inv := r.inv
	delimiter := byte('\n')
	if inv.flag("-z") {
		delimiter = 0
	}
	writers, err := r.openWrites()
	if err != nil {
		return sedscript.Config{}, err
	}
	files, err := r.readFileFactory()
	if err != nil {
		return sedscript.Config{}, err
	}
	return sedscript.Config{
		Quiet: inv.flag("-n") || r.program.Quiet(), Delimiter: delimiter,
		SpaceLimit: inv.limit("record_bytes"), StepLimit: inv.limit("query_work_steps"), Reserve: inv.reserve,
		Stdout: inv.out, WriteLiteral: inv.out.WriteLiteral, Stderr: inv.streams.Error, Writers: writers, OpenFile: files,
	}, nil
}

// failure projects the language's limits and faults onto catalog refusals.
func (r *sedRun) failure(err error) error {
	var limit *sedscript.LimitError
	var runtime *sedscript.RuntimeError
	var budget *textsearch.BudgetError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &limit):
		return errors.Join(err, tools.CommandLimitExceeded(limit.Resource, limit.Maximum, limit.Observed))
	case errors.As(err, &budget):
		return errors.Join(err, tools.CommandLimitExceeded(budget.Resource, budget.Maximum, budget.Observed))
	case errors.As(err, &runtime):
		return errors.Join(err, r.inv.usage(r.word, runtime.Reason))
	}
	return err
}

type sedSource struct {
	text     string
	word     commandprogram.Field
	files    commandprogram.Fields
	fromFile bool
	inPlace  bool
	suffix   string
}

// prepareSed builds the single admitted prepared plan for sed. Inline scripts
// (-e or positional SCRIPT) are parsed once here, selecting both BSD -i suffix
// disambiguation and the catalog effect mode from that one AST. Forms with -f
// defer file capture until the child receipt exists in runSed.
func (o *Owners) prepareSed(ctx context.Context, spec toolcontract.CommandSpecification, request commandprogram.OperationRequest, bound commandprogram.BoundArgv) (*preparedSed, string, error) {
	if o.Regex == nil {
		return nil, "", toolrejection.RejectInvalidArguments("COMMAND_PLATFORM_UNAVAILABLE", map[string]any{"command": "sed", "capability": "the shared POSIX regex engine"})
	}
	if o.Revisions.Memory == nil {
		return nil, "", errors.New("text utilities require program memory admission")
	}
	operands := bound.Operands()
	var out sedSource
	for _, key := range []string{"-i", "-I"} {
		if option, ok := bound.Last(key); ok {
			out.inPlace = true
			if len(option.Values) > 0 {
				out.suffix = option.Values[0].Value
			}
		}
	}
	// BSD sed spells an empty in-place backup suffix as a separate argument.
	// With -e/-f, that empty argument precedes the file operands and cannot be
	// mistaken for a file name.
	if option, ok := bound.Last("-i"); ok && len(option.Values) == 0 && len(operands) > 0 && operands[0].Value == "" && (len(bound.Options("-e")) > 0 || len(bound.Options("-f")) > 0) {
		operands = operands[1:]
	}
	var parts []string
	for _, item := range bound.Items {
		option := item.Option
		if option == nil || (option.Key != "-e" && option.Key != "-f") {
			continue
		}
		value := option.Values[0]
		if len(parts) == 0 && !out.fromFile {
			out.word = value
		}
		if option.Key == "-e" {
			parts = append(parts, value.Value)
			continue
		}
		if value.Value == "-" {
			return nil, "", nativeutility.Usage(spec, value, "a script file; standard input is the data")
		}
		out.fromFile = true
	}
	if out.fromFile {
		out.files = operands
		if out.inPlace && len(out.files) == 0 {
			return nil, "", nativeutility.Usage(spec, request.Argv[0], "a PATH to edit in place")
		}
		mode := "stream-script"
		if out.inPlace {
			mode = "-i"
		}
		return &preparedSed{source: out}, mode, nil
	}
	compiler := sedCompiler{o.Regex}
	extended := argvFlag(bound, "-E")
	var program *sedscript.Program
	var err error
	if len(parts) > 0 {
		out.text, out.files = strings.Join(parts, "\n"), operands
		program, err = sedscript.Parse(ctx, o.Revisions.Memory, out.text, extended, compiler)
		if err != nil {
			return nil, "", sedRefusalSpec(spec, out.word, err)
		}
	} else {
		if len(operands) == 0 {
			return nil, "", nativeutility.Usage(spec, request.Argv[0], "a SCRIPT")
		}
		program, err = o.parsePositionalSed(ctx, spec, bound, operands, extended, compiler, &out)
		if err != nil {
			return nil, "", err
		}
	}
	if out.inPlace && len(out.files) == 0 {
		program.Close()
		return nil, "", nativeutility.Usage(spec, request.Argv[0], "a PATH to edit in place")
	}
	mode := "stream-script"
	if out.inPlace {
		mode = "-i"
	} else if len(program.WriteFiles()) > 0 {
		mode = "w-command"
	}
	return &preparedSed{source: out, program: program}, mode, nil
}

// parsePositionalSed parses the positional script operand once, disambiguating
// BSD sed -i SUFFIX SCRIPT [FILE ...] when -i took no attached value and the
// first operand fails syntax while the second is a valid script. Resource and
// cancellation failures propagate immediately without falling through.
func (o *Owners) parsePositionalSed(ctx context.Context, spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv, operands commandprogram.Fields, extended bool, compiler sedCompiler, out *sedSource) (*sedscript.Program, error) {
	firstProg, firstErr := sedscript.Parse(ctx, o.Revisions.Memory, operands[0].Value, extended, compiler)
	if firstErr == nil {
		out.text, out.word, out.files = operands[0].Value, operands[0], operands[1:]
		return firstProg, nil
	}
	if isResourceOrContextError(firstErr) {
		return nil, sedRefusalSpec(spec, operands[0], firstErr)
	}
	option, hasInPlace := bound.Last("-i")
	bsdCandidate := hasInPlace && len(option.Values) == 0 && len(operands) >= 2 && !argvFlag(bound, "-e") && !argvFlag(bound, "-f") && !argvFlag(bound, "-I")
	if bsdCandidate {
		secondProg, secondErr := sedscript.Parse(ctx, o.Revisions.Memory, operands[1].Value, extended, compiler)
		if secondErr == nil {
			out.suffix = operands[0].Value
			out.text, out.word, out.files = operands[1].Value, operands[1], operands[2:]
			return secondProg, nil
		}
		if isResourceOrContextError(secondErr) {
			return nil, sedRefusalSpec(spec, operands[1], secondErr)
		}
	}
	return nil, sedRefusalSpec(spec, operands[0], firstErr)
}

func loadSedFileScript(inv *invocation, out sedSource) (sedSource, *sedscript.Program, error) {
	var parts []string
	var totalBytes int64
	for _, item := range inv.argv.Items {
		option := item.Option
		if option == nil || (option.Key != "-e" && option.Key != "-f") {
			continue
		}
		value := option.Values[0]
		if len(parts) == 0 {
			out.word = value
		}
		if option.Key == "-e" {
			parts = append(parts, value.Value)
			totalBytes += int64(len(value.Value)) + 1
			continue
		}
		text, err := readScriptFile(inv, value)
		if err != nil {
			return out, nil, err
		}
		trimmed := strings.TrimSuffix(text, "\n")
		parts = append(parts, trimmed)
		totalBytes += int64(len(trimmed)) + 1
		out.fromFile = true
	}
	if len(parts) > 1 {
		if err := inv.reserve(totalBytes); err != nil {
			return out, nil, err
		}
	}
	out.text = strings.Join(parts, "\n")
	program, err := sedscript.Parse(inv.ctx, inv.ports.Memory, out.text, inv.flag("-E"), sedCompiler{inv.owners.Regex})
	if err != nil {
		return out, nil, sedRefusal(inv, out.word, err)
	}
	return out, program, nil
}

// readScriptFile reads one -f script through the pinned capture of any input.
func readScriptFile(inv *invocation, word commandprogram.Field) (script string, finalErr error) {
	if word.Value == "-" {
		return "", inv.usage(word, "a script file; standard input is the data")
	}
	paths, err := newPaths(inv)
	if err != nil {
		return "", err
	}
	reader, _, err := inv.capture(paths, word)
	if err != nil {
		if reason, ok := ordinary(err); ok {
			return "", inv.usage(word, "a readable script file: "+reason)
		}
		return "", pathRejection(err, word)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			if finalErr == nil {
				finalErr = closeErr
			} else {
				finalErr = errors.Join(finalErr, closeErr)
			}
		}
	}()
	revisions := inv.owners.Revisions
	if inv.ports.Memory != nil {
		revisions.Memory = inv.ports.Memory
	}
	captured, err := revisions.ReadWholeInput(inv.ctx, reader, inv.limit("whole_file_bytes"))
	if err != nil {
		return "", err
	}
	defer captured.Close()
	if err := inv.reserve(int64(len(captured.Bytes))); err != nil {
		return "", err
	}
	return string(captured.Bytes), nil
}

func isResourceOrContextError(err error) bool {
	if err == nil {
		return false
	}
	var memErr *toolcontract.ProgramMemoryLimitError
	var budgetErr *textsearch.BudgetError
	var limitErr *sedscript.LimitError
	return errors.As(err, &memErr) || errors.As(err, &budgetErr) || errors.As(err, &limitErr) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func sedRefusal(inv *invocation, word commandprogram.Field, err error) error {
	return sedRefusalSpec(inv.spec, word, err)
}

func sedRefusalSpec(spec toolcontract.CommandSpecification, word commandprogram.Field, err error) error {
	if err == nil {
		return nil
	}
	var limitErr *sedscript.LimitError
	if errors.As(err, &limitErr) {
		return errors.Join(err, tools.CommandLimitExceeded(limitErr.Resource, limitErr.Maximum, limitErr.Observed))
	}
	var budgetErr *textsearch.BudgetError
	if errors.As(err, &budgetErr) {
		return errors.Join(err, tools.CommandLimitExceeded(budgetErr.Resource, budgetErr.Maximum, budgetErr.Observed))
	}
	var memErr *toolcontract.ProgramMemoryLimitError
	if errors.As(err, &memErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return tools.CommandResourceRejection(err)
	}
	var script *sedscript.ScriptError
	if errors.As(err, &script) {
		if script.Unsupported {
			// Script forms outside the language refuse as unsupported options.
			return toolrejection.RejectInvalidArguments("COMMAND_OPTION_UNSUPPORTED", map[string]any{
				"command": "sed", "word": word.Value, "expected": script.Error(), "expected_form": spec.Argv,
				"source_start_byte": word.Span.StartByte, "source_end_byte": word.Span.EndByte, "source_line": word.Span.Line, "source_column": word.Span.Column,
			})
		}
		return nativeutility.Usage(spec, word, script.Error())
	}
	var regex *sedscript.RegexError
	if errors.As(err, &regex) {
		return errors.Join(err, nativeutility.Usage(spec, word, "a valid regex at script byte "+strconv.Itoa(regex.Offset)))
	}
	return err
}

func argvFlag(argv commandprogram.BoundArgv, key string) bool {
	_, ok := argv.Last(key)
	return ok
}
