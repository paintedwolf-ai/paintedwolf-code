package textutil

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolrejection"
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

func (c sedCompiler) Compile(ctx context.Context, pattern string, extended, fold bool) (sedscript.Regex, error) {
	if fold {
		return c.engine.CompileFolded(ctx, pattern, extended)
	}
	return c.engine.Compile(ctx, pattern, extended)
}

// runSed parses the complete script before any input is read, opens every w
// file and reads every r file, then runs the inputs as one stream, one stream
// per file with -s, or edits each file in place with -i. Output preserves a
// final record's missing newline; q and Q set the exit status.
func runSed(inv *invocation) error {
	if inv.owners.Regex == nil {
		return toolrejection.RejectInvalidArguments("COMMAND_PLATFORM_UNAVAILABLE", map[string]any{"command": "sed", "capability": "the shared POSIX regex engine"})
	}
	script, err := sedScript(inv)
	if err != nil {
		return err
	}
	program, err := sedscript.Parse(inv.ctx, script.text, inv.flag("-E"), sedCompiler{inv.owners.Regex})
	if err != nil {
		return sedRefusal(inv, script.word, err)
	}
	if script.fromFile && len(program.WriteFiles()) > 0 {
		return unsupported(inv, script.word, "w files named in an inline script (-e or SCRIPT), so the write is reviewed with the command")
	}
	run := &sedRun{inv: inv, program: program, word: script.word, stages: map[string]commandprogram.StagedOutput{}}
	err = run.execute(script)
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
		_, err := r.machine.Stream(r.input(sources), nil)
		return r.failure(err)
	}
	for _, input := range sources {
		if quit, err := r.machine.Stream(r.input([]source{input}), nil); err != nil || quit {
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
	files, err := r.readFiles()
	if err != nil {
		return sedscript.Config{}, err
	}
	return sedscript.Config{
		Quiet: inv.flag("-n") || r.program.Quiet(), Delimiter: delimiter,
		SpaceLimit: inv.limit("record_bytes"), StepLimit: inv.limit("query_work_steps"), Reserve: inv.reserve,
		Stdout: inv.out, Stderr: inv.streams.Error, Writers: writers, Files: files,
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
		return errors.Join(err, nativeutility.LimitExceeded(limit.Resource, limit.Maximum, limit.Observed))
	case errors.As(err, &budget):
		return errors.Join(err, nativeutility.LimitExceeded(budget.Resource, budget.Maximum, budget.Observed))
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

// sedScript joins -e scripts and -f files with newlines in argv order, or
// takes the first operand when neither is given, and resolves the in-place
// suffix: GNU -i[SUFFIX], BSD -i ” and -I SUFFIX, and BSD -i SUFFIX SCRIPT,
// recognized when the word after -i is not a script and the next one is.
func sedScript(inv *invocation) (sedSource, error) {
	operands := inv.operands()
	var parts []string
	var out sedSource
	for _, key := range []string{"-i", "-I"} {
		if option, ok := inv.argv.Last(key); ok {
			out.inPlace = true
			if len(option.Values) > 0 {
				out.suffix = option.Values[0].Value
			}
		}
	}
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
			continue
		}
		text, err := readScriptFile(inv, value)
		if err != nil {
			return out, err
		}
		parts = append(parts, strings.TrimSuffix(text, "\n"))
		out.fromFile = true
	}
	if len(parts) > 0 {
		out.text, out.files = strings.Join(parts, "\n"), operands
		return out, nil
	}
	if len(operands) == 0 {
		return out, inv.usage(inv.commandWord(), "a SCRIPT")
	}
	if inv.owners.bsdSuffix(inv.ctx, inv.argv, operands) {
		out.suffix, operands = operands[0].Value, operands[1:]
	}
	out.text, out.word, out.files = operands[0].Value, operands[0], operands[1:]
	return out, nil
}

// bsdSuffix reports BSD sed -i SUFFIX SCRIPT [FILE ...]: -i took no value, no
// -e or -f gives the script, and the first operand is not a valid script
// while the second is. Otherwise the GNU reading stands.
func (o *Owners) bsdSuffix(ctx context.Context, argv commandprogram.BoundArgv, operands commandprogram.Fields) bool {
	option, ok := argv.Last("-i")
	if !ok || len(option.Values) > 0 || len(operands) < 2 || argvFlag(argv, "-e") || argvFlag(argv, "-f") || argvFlag(argv, "-I") || o.Regex == nil {
		return false
	}
	compiler, extended := sedCompiler{o.Regex}, argvFlag(argv, "-E")
	if _, err := sedscript.Parse(ctx, operands[0].Value, extended, compiler); err == nil {
		return false
	}
	_, err := sedscript.Parse(ctx, operands[1].Value, extended, compiler)
	return err == nil
}

// readScriptFile reads one -f script through the pinned capture of any input.
func readScriptFile(inv *invocation, word commandprogram.Field) (string, error) {
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
	defer func() { _ = reader.Close() }()
	text, err := io.ReadAll(reader)
	return string(text), err
}

func sedRefusal(inv *invocation, word commandprogram.Field, err error) error {
	var script *sedscript.ScriptError
	if errors.As(err, &script) {
		if script.Unsupported {
			// Script forms outside the language refuse as unsupported options.
			return toolrejection.RejectInvalidArguments("COMMAND_OPTION_UNSUPPORTED", map[string]any{
				"command": "sed", "word": word.Value, "expected": script.Error(), "expected_form": inv.spec.Argv,
				"source_start_byte": word.Span.StartByte, "source_end_byte": word.Span.EndByte, "source_line": word.Span.Line, "source_column": word.Span.Column,
			})
		}
		return inv.usage(word, script.Error())
	}
	var regex *sedscript.RegexError
	if errors.As(err, &regex) {
		return errors.Join(err, inv.usage(word, "a valid regex at script byte "+strconv.Itoa(regex.Offset)))
	}
	return err
}

// sedMode names the catalog effect mode a bound sed form selects: -i and
// inline w files write; every other form reads.
func (o *Owners) sedMode(ctx context.Context, argv commandprogram.BoundArgv) string {
	if argvFlag(argv, "-i") || argvFlag(argv, "-I") {
		return "-i"
	}
	if o.Regex == nil {
		return "stream-script"
	}
	if _, ok := argv.Last("-f"); ok {
		// Writes named in script files refuse at run time.
		return "stream-script"
	}
	var parts []string
	for _, item := range argv.Items {
		if option := item.Option; option != nil && option.Key == "-e" {
			parts = append(parts, option.Values[0].Value)
		}
	}
	if operands := argv.Operands(); len(parts) == 0 && len(operands) > 0 {
		parts = append(parts, operands[0].Value)
	}
	if program, err := sedscript.Parse(ctx, strings.Join(parts, "\n"), argvFlag(argv, "-E"), sedCompiler{o.Regex}); err == nil && len(program.WriteFiles()) > 0 {
		return "w-command"
	}
	return "stream-script"
}

func argvFlag(argv commandprogram.BoundArgv, key string) bool {
	_, ok := argv.Last(key)
	return ok
}
