package query

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/mikefarah/yq/v4/pkg/yqlib"
)

// yqlib keeps its preferences, expression parser and filename state in
// process globals, so evaluations run one at a time. Environment, file and
// system operators would reach the host process; they stay disabled.
var (
	yqEvaluation sync.Mutex
	yqSetup      sync.Once
)

func setupYq() {
	yqlib.InitExpressionParser()
	yqlib.ConfiguredSecurityPreferences = yqlib.SecurityPreferences{DisableEnvOps: true, DisableFileOps: true}
}

// yqRun is one mikefarah yq v4 invocation: `yq [eval|eval-all] [flags]
// [EXPRESSION] [FILE ...]`.
type yqRun struct {
	inv        *invocation
	all        bool
	expression string
	files      commandprogram.Fields
	input      string
	output     string
	// sources holds each file captured once, so -i reads and publishes
	// against the same revision.
	sources map[commandprogram.Field]*source
}

// runYq evaluates a yq expression with the yq v4 library. Errors print
// "Error: ..." and exit 1, as yq does; -i publishes the first file's result
// at its captured revisions.
func runYq(inv *invocation) error {
	if inv.pythonYq() {
		return runPythonYq(inv)
	}
	yqSetup.Do(setupYq)
	run, err := newYqRun(inv)
	if err != nil {
		return err
	}
	if run == nil {
		return nil
	}
	yqEvaluation.Lock()
	defer yqEvaluation.Unlock()
	if inv.flag("-i") {
		return run.inPlace()
	}
	printer, err := run.printer(inv.out)
	if err != nil {
		return run.fail(err)
	}
	if err := run.evaluate(printer); err != nil {
		return run.fail(err)
	}
	if inv.flag("-e") && !printer.PrintedAnything() {
		return run.fail(errors.New("no matches found"))
	}
	return nil
}

// newYqRun applies yq's argument rules: an optional eval/eval-all word, then
// the expression unless the first word is "-" or is classified as a file.
func newYqRun(inv *invocation) (*yqRun, error) {
	operands := inv.argv.Operands()
	run := &yqRun{inv: inv, sources: map[commandprogram.Field]*source{}}
	if len(operands) > 0 {
		switch operands[0].Value {
		case "eval", "e":
			operands = operands[1:]
		case "eval-all", "ea":
			run.all, operands = true, operands[1:]
		}
	}
	if expression, ok := inv.value("--expression"); ok {
		run.expression = expression
	} else if len(operands) > 0 && operands[0].Value != "-" && operands[0].ProcessSubstitution() == nil && !yqImplicitFile(inv.ctx, inv.paths, inv.argv, operands[0].Value) {
		run.expression, operands = operands[0].Value, operands[1:]
	}
	if inv.flag("-P") {
		if run.expression == "" {
			run.expression = yqlib.PrettyPrintExp
		} else {
			run.expression += " | " + yqlib.PrettyPrintExp
		}
	}
	run.files = operands
	switch {
	case inv.flag("-i") && (len(run.files) == 0 || run.files[0].Value == "-"):
		return nil, run.fail(errors.New("write in place flag only applicable when giving an expression and at least one file"))
	case inv.flag("-n") && len(run.files) > 0:
		return nil, run.fail(errors.New("cannot pass files in when using null-input flag"))
	case len(run.files) == 0 && !inv.flag("-n"):
		run.files = commandprogram.Fields{{Value: "-"}}
	}
	if err := run.formats(); err != nil {
		return nil, run.fail(err)
	}
	return run, nil
}

// formats resolves -p and -o: auto input follows the first file's
// extension (unknown is YAML) and auto output follows the input.
func (r *yqRun) formats() error {
	r.input = yqOption(r.inv, "-p", "auto")
	r.output = yqOption(r.inv, "-o", "auto")
	if r.inv.flag("-j") {
		_, _ = io.WriteString(r.inv.stderr, "Flag --tojson has been deprecated, please use -o=json instead\n")
		r.output = "json"
	}
	automatic := func(format string) bool { return format == "" || format == "auto" || format == "a" }
	first := ""
	if len(r.files) > 0 {
		first = r.files[0].Value
	}
	if automatic(r.input) {
		r.input = yqlib.FormatStringFromFilename(first)
		if _, err := yqlib.FormatFromString(r.input); err != nil {
			r.input = "yaml"
		}
		if automatic(r.output) {
			r.output = r.input
		}
	} else if automatic(r.output) {
		r.output = "yaml"
	}
	for _, format := range []string{r.input, r.output} {
		if _, err := yqlib.FormatFromString(format); err != nil {
			return err
		}
	}
	return nil
}

// yqOption is a value flag; yq's shorthand also accepts -o=json.
func yqOption(inv *invocation, key, fallback string) string {
	value, ok := inv.value(key)
	if !ok {
		return fallback
	}
	return strings.TrimPrefix(value, "=")
}

// yqImplicitFile decides whether the first positional operand is a file.
// Explicit path syntax is a file identity before path authority is checked;
// otherwise yq's existing regular-file rule distinguishes a bare filename
// from an expression. Null input takes the first operand as an expression.
func yqImplicitFile(ctx context.Context, paths *projectpaths.ProgramPaths, argv commandprogram.BoundArgv, operand string) bool {
	if _, nullInput := argv.Last("-n"); nullInput {
		return false
	}
	if filepath.IsAbs(operand) || strings.HasPrefix(operand, "./") || strings.HasPrefix(operand, "../") || operand == "@scratch" || strings.HasPrefix(operand, "@scratch/") {
		return true
	}
	return regularYqFile(ctx, paths, operand)
}

// regularYqFile is yq's maybeFile for an unmarked first operand.
func regularYqFile(ctx context.Context, paths *projectpaths.ProgramPaths, operand string) bool {
	bound, err := paths.Bind(ctx, operand, false)
	if err != nil || bound.Null {
		return false
	}
	stat, err := bound.Stat(true)
	return err == nil && stat.Mode.IsRegular()
}

// fail reports an ordinary yq error; typed refusals and interruption stop
// the run instead.
func (r *yqRun) fail(err error) error {
	if toolrejection.AsToolReject(err) != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, commandprogram.ErrConsumerClosed) {
		return err
	}
	_, _ = fmt.Fprintf(r.inv.stderr, "Error: %s\n", err)
	r.inv.status = 1
	return nil
}

// printer encodes results in the output format with yq's flags.
func (r *yqRun) printer(out io.Writer) (yqlib.Printer, error) {
	format, err := yqlib.FormatFromString(r.output)
	if err != nil {
		return nil, err
	}
	indent := 2
	if text, ok := r.inv.value("-I"); ok {
		if indent, err = strconv.Atoi(strings.TrimPrefix(text, "=")); err != nil || indent < 0 {
			return nil, errors.New("indent must not be negative")
		}
	}
	unwrap := format == yqlib.YamlFormat || format == yqlib.PropertiesFormat
	if value, ok := r.inv.argv.Last("-r"); ok {
		unwrap = len(value.Values) == 0 || strings.TrimPrefix(value.Values[0].Value, "=") != "false"
	}
	var encoder yqlib.Encoder
	switch format {
	case yqlib.YamlFormat:
		prefs := yqlib.ConfiguredYamlPreferences.Copy()
		prefs.Indent, prefs.UnwrapScalar, prefs.ColorsEnabled, prefs.PrintDocSeparators = indent, unwrap, false, !r.inv.flag("-N")
		encoder = yqlib.NewYamlEncoder(prefs)
	case yqlib.JSONFormat:
		encoder = yqlib.NewJSONEncoder(yqlib.JsonPreferences{Indent: indent, UnwrapScalar: unwrap})
	default:
		if format.EncoderFactory == nil {
			return nil, fmt.Errorf("no support for %s output format", r.output)
		}
		encoder = format.EncoderFactory()
	}
	if encoder == nil {
		return nil, fmt.Errorf("no support for %s output format", r.output)
	}
	printer := yqlib.NewPrinter(encoder, yqlib.NewSinglePrinterWriter(out))
	if r.inv.flag("-0") {
		printer.SetNulSepOutput(true)
	}
	return printer, nil
}

func (r *yqRun) decoder() (yqlib.Decoder, error) {
	format, err := yqlib.FormatFromString(r.input)
	if err != nil {
		return nil, err
	}
	if format == yqlib.YamlFormat {
		prefs := yqlib.ConfiguredYamlPreferences.Copy()
		prefs.EvaluateTogether = r.all
		return yqlib.NewYamlDecoder(prefs), nil
	}
	if format.DecoderFactory == nil {
		return nil, fmt.Errorf("no support for %s input format", r.input)
	}
	decoder := format.DecoderFactory()
	if decoder == nil {
		return nil, fmt.Errorf("no support for %s input format", r.input)
	}
	return decoder, nil
}

// evaluate runs the expression per document (eval) or once over every
// document (eval-all); with no documents it runs once over null.
func (r *yqRun) evaluate(printer yqlib.Printer) error {
	evaluator := yqlib.NewStreamEvaluator()
	if r.inv.flag("-n") {
		return evaluator.EvaluateNew(r.expression, printer)
	}
	decoder, err := r.decoder()
	if err != nil {
		return err
	}
	node, err := yqlib.ExpressionParser.ParseExpression(r.expression)
	if err != nil {
		return err
	}
	documents := list.New()
	var processed uint
	for index, file := range r.files {
		reader, label, err := r.reader(file)
		if err != nil {
			return err
		}
		if !r.all {
			count, err := evaluator.Evaluate(label, reader, node, printer, decoder)
			processed += count
			if err != nil {
				return err
			}
			continue
		}
		if err := readYqDocuments(documents, reader, label, index, decoder); err != nil {
			return err
		}
	}
	if !r.all {
		if processed == 0 {
			return evaluator.EvaluateNew(r.expression, printer)
		}
		return nil
	}
	if documents.Len() == 0 {
		return evaluator.EvaluateNew(r.expression, printer)
	}
	matches, err := yqlib.NewAllAtOnceEvaluator().EvaluateCandidateNodes(r.expression, documents)
	if err != nil {
		return err
	}
	return printer.PrintResults(matches)
}

// readYqDocuments decodes one file for eval-all, marking each document with
// its file as yq's own reader does.
func readYqDocuments(documents *list.List, reader io.Reader, label string, fileIndex int, decoder yqlib.Decoder) error {
	if err := decoder.Init(reader); err != nil {
		return err
	}
	for index := uint(0); ; index++ {
		node, err := decoder.Decode()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("bad file '%v': %w", label, err)
		}
		node.SetDocument(index)
		node.SetFilename(label)
		node.SetFileIndex(fileIndex)
		node.EvaluateTogether = true
		documents.PushBack(node)
	}
}

// capture pins a file once; an unreadable file is yq's open error.
func (r *yqRun) capture(file commandprogram.Field) (*source, error) {
	if captured, ok := r.sources[file]; ok {
		return captured, nil
	}
	captured, err := r.inv.captureSource(file)
	var failure *unreadable
	if errors.As(err, &failure) {
		return nil, fmt.Errorf("open %s: %s", file.Value, strings.ToLower(failure.reason))
	}
	if err != nil {
		return nil, err
	}
	r.sources[file] = captured
	return captured, nil
}

// inPlace evaluates into the first file and publishes the result at the
// revisions it read, as jq-edit does; unchanged text publishes nothing.
func (r *yqRun) inPlace() error {
	target := r.files[0]
	bound, err := r.inv.paths.Bind(r.inv.ctx, target.Value, true)
	if err != nil {
		return err
	}
	if bound.Null {
		return r.inv.usage(target.Value, "a document PATH for -i")
	}
	source, err := r.capture(target)
	if err != nil {
		return r.fail(err)
	}
	destination, err := r.inv.owners.Revisions.CaptureDestination(r.inv.ctx, r.inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := r.inv.recordPin(destination); err != nil {
		return err
	}
	result, err := r.inv.owners.Revisions.NewBufferedBytes(r.inv.ctx, r.inv.owners.limit("whole_file_bytes"), "whole_file_bytes")
	if err != nil {
		return err
	}
	defer result.Close()
	printer, err := r.printer(result)
	if err != nil {
		return r.fail(err)
	}
	if err := r.evaluate(printer); err != nil {
		return r.fail(err)
	}
	if string(result.Bytes) == source.text {
		return nil
	}
	return r.inv.publishBytes(source, destination, result.Bytes)
}
