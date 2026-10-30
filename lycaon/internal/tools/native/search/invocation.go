package search

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// invocation is one utility run: its bound argv, streams and pins.
type invocation struct {
	ctx      context.Context
	owners   *Owners
	tc       tools.ToolContext
	paths    *projectpaths.ProgramPaths
	dispatch commandprogram.Dispatch
	request  commandprogram.OperationRequest
	spec     toolcontract.CommandSpecification
	argv     commandprogram.BoundArgv
	input    io.Reader
	stderr   io.Writer
	out      *output
	// status is the worst ordinary status so far; failure is the status a
	// failure reports (grep and rg use 2, find 1).
	status  int
	failure int
	// noMatchPublishes marks a complete status-one search as publishable.
	noMatchPublishes bool
	effects          toolcontract.EffectCertainty
	pins             []toolcontract.ExecutionRevision
}

func newInvocation(ctx context.Context, p Prepared, tc tools.ToolContext, streams commandprogram.Streams) *invocation {
	inv := &invocation{ctx: ctx, owners: p.owners, tc: tc, paths: p.paths, dispatch: p.dispatch, request: p.request, spec: p.spec, argv: p.Argv,
		input: streams.Input, stderr: streams.Error, failure: 2, effects: toolcontract.NoEffect}
	if inv.input == nil {
		inv.input = commandprogram.NullInput()
	}
	if inv.stderr == nil {
		inv.stderr = io.Discard
	}
	inv.out = newOutput(ctx, streams.Output, p.owners.streamBytes())
	return inv
}

// word is argv[0] as spelled, for diagnostics ("egrep: ...").
func (inv *invocation) word() string { return inv.request.Argv[0].Value }

func (inv *invocation) flag(key string) bool {
	_, ok := inv.argv.Last(key)
	return ok
}

func (inv *invocation) value(key string) (string, bool) {
	option, ok := inv.argv.Last(key)
	if !ok || len(option.Values) == 0 {
		return "", false
	}
	return option.Values[0].Value, true
}

func (inv *invocation) values(key string) []string {
	var out []string
	for _, option := range inv.argv.Options(key) {
		if len(option.Values) > 0 {
			out = append(out, option.Values[0].Value)
		}
	}
	return out
}

// warn writes one diagnostic line; quiet suppresses it (grep -s).
func (inv *invocation) warn(quiet bool, format string, args ...any) {
	if quiet {
		return
	}
	_, _ = fmt.Fprintf(inv.stderr, "%s: "+format+"\n", append([]any{inv.word()}, args...)...)
}

// fail records an ordinary failure, reported unless quiet.
func (inv *invocation) fail(quiet bool, format string, args ...any) {
	inv.warn(quiet, format, args...)
	inv.status = max(inv.status, inv.failure)
}

// errUsage ends an invocation that already reported an ordinary usage or
// pattern error on stderr.
var errUsage = errors.New("search usage error")

// usage reports an invalid argument, expression or option value on stderr and
// stops the invocation with its ordinary failure status (2 for grep/rg, 1 for
// find).
func (inv *invocation) usage(word, reason string) error {
	if word == "" || word == inv.word() {
		inv.fail(false, "%s", reason)
	} else {
		inv.fail(false, "%s: %s", word, reason)
	}
	return errUsage
}

func capitalizeFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// limitExceeded refuses work above a frozen catalog limit; nothing is truncated.
func limitExceeded(id string, maximum, observed int64) error {
	return &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": id, "resource_unit": nativeutility.ResourceUnit(id), "maximum": maximum, "observed": observed}}
}

// ordinary classifies an input failure the utility reports on stderr and
// survives. Authority, budget and platform refusals stay typed stops.
func ordinary(err error) (string, bool) {
	if reject := toolrejection.AsToolReject(err); reject != nil {
		switch reject.Code {
		case "READ_PATH_NOT_FOUND":
			return "No such file or directory", true
		case "READ_IS_DIRECTORY":
			return "Is a directory", true
		}
		return "", false
	}
	var pathErr *projectpaths.PathError
	if errors.As(err, &pathErr) {
		return "", false
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "No such file or directory", true
	case errors.Is(err, fs.ErrPermission):
		return "Permission denied", true
	}
	return "", false
}

// patternRejection carries a search pattern or budget stop as its public code.
func patternRejection(err error) error {
	var pattern *textsearch.PatternError
	if errors.As(err, &pattern) {
		return errors.Join(err, &toolrejection.ToolReject{Code: pattern.Code, Data: map[string]any{
			"pattern": pattern.Pattern, "offset": pattern.Offset, "detail": pattern.Detail,
		}})
	}
	var budget *textsearch.BudgetError
	if errors.As(err, &budget) {
		return errors.Join(err, &toolrejection.ToolReject{Code: textsearch.CodeMatchBudget, Data: map[string]any{
			"limit_id": budget.Resource, "maximum": budget.Maximum, "observed": budget.Observed,
		}})
	}
	return err
}

// recordPin retains every capture of this execution before its bytes are used.
func (inv *invocation) recordPin(revision toolcontract.ExecutionRevision) error {
	execution := inv.request.Invocation.Identity.ExecutionID
	if inv.owners.Pins == nil || execution == "" {
		return fmt.Errorf("search reads require a program execution and pin recorder")
	}
	inv.pins = append(inv.pins, revision)
	return inv.owners.Pins.RecordRevisionPins(inv.ctx, execution, len(inv.pins)-1, inv.pins[len(inv.pins)-1:])
}

// capture pins one file operand's exact bytes before any are searched.
func (inv *invocation) capture(bound projectpaths.BoundPath) ([]byte, error) {
	content, _, err := inv.capturePinned(bound)
	return content, err
}

// capturePinned also returns the stream-mapping identity of the capture.
func (inv *invocation) capturePinned(bound projectpaths.BoundPath) ([]byte, *toolcontract.SourcePin, error) {
	captured, err := inv.owners.Revisions.CaptureBytes(inv.ctx, inv.tc, bound.Resolved.Abs, inv.owners.readBytes())
	if err != nil {
		return nil, nil, err
	}
	if err := inv.recordPin(captured.Revision); err != nil {
		return nil, nil, err
	}
	if pin, ok := captured.Revision.SourcePin(); ok {
		return captured.Bytes, &pin, nil
	}
	return captured.Bytes, nil, nil
}

// readAll gathers stdin whole, refusing rather than truncating input above
// stream_bytes.
func (inv *invocation) readAll(r io.Reader) ([]byte, error) {
	maximum := inv.owners.streamBytes()
	data, err := io.ReadAll(io.LimitReader(r, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, limitExceeded("stream_bytes", maximum, int64(len(data)))
	}
	return data, nil
}

// settle projects the run onto the producer contract.
func (inv *invocation) settle(err error) (toolcontract.ProducerResult, error) {
	var pattern *textsearch.PatternError
	if errors.As(err, &pattern) && pattern.Code == textsearch.CodeRegexInvalid {
		if inv.spec.Name == "find" {
			inv.fail(false, "failed to compile regular expression '%s': %s", pattern.Pattern, capitalizeFirst(pattern.Detail))
		} else {
			inv.fail(false, "%s", capitalizeFirst(pattern.Detail))
		}
		err = nil
	} else if errors.Is(err, errUsage) {
		err = nil
	}
	err = projectpaths.WithPathRejection(patternRejection(err), inv.request.Invocation.Span)
	if err != nil {
		result := toolcontract.ProducerResult{Status: inv.failure, Terminal: toolcontract.ProducerFailed, EffectCertainty: inv.effects}
		if reject := toolrejection.AsToolReject(err); reject != nil {
			result.Status, result.Terminal, result.StopCode = 2, toolcontract.ProducerRefused, reject.Code
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Status, result.Terminal, result.StopCode = 130, toolcontract.ProducerInterrupted, ""
		}
		if inv.effects != toolcontract.NoEffect {
			result.EffectCertainty = toolcontract.EffectUnknown
		}
		return result, err
	}
	result := toolcontract.ProducerResult{Status: inv.status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: inv.effects}
	switch {
	case inv.status == 0:
		result.PublicationEligible = true
	case inv.noMatchPublishes && inv.status == 1:
		result.PublicationEligible, result.PublicationBasis = true, toolcontract.NativeComparisonPublication
	}
	return result, nil
}

// output meters stdout against stream_bytes before each write and, when the
// endpoint retains them, records exact source mappings for bytes copied from
// a pinned capture. Prefixes, separators and replacements carry none.
type output struct {
	ctx              context.Context
	w                io.Writer
	mappings         commandprogram.SourceMappingRecorder
	pending          *toolcontract.StreamSourceMapping
	maximum, written int64
}

func newOutput(ctx context.Context, w io.Writer, maximum int64) *output {
	if w == nil {
		w = io.Discard
	}
	o := &output{ctx: ctx, w: w, maximum: maximum}
	o.mappings, _ = w.(commandprogram.SourceMappingRecorder)
	return o
}

// literal writes bytes taken verbatim from a capture at offset and maps them.
// Adjacent ranges of one capture coalesce into one mapping.
func (o *output) literal(pin *toolcontract.SourcePin, p []byte, offset int64) error {
	if len(p) == 0 {
		return nil
	}
	outStart := o.written
	if _, err := o.Write(p); err != nil {
		return err
	}
	if o.mappings == nil || pin == nil {
		return nil
	}
	next := toolcontract.StreamSourceMapping{Source: *pin, SourceStartByte: offset, SourceEndByte: offset + int64(len(p)), OutputStartByte: outStart, OutputEndByte: o.written}
	if last := o.pending; last != nil && last.Source == next.Source && last.SourceEndByte == next.SourceStartByte && last.OutputEndByte == next.OutputStartByte {
		last.SourceEndByte, last.OutputEndByte = next.SourceEndByte, next.OutputEndByte
		return nil
	}
	if err := o.flush(); err != nil {
		return err
	}
	o.pending = &next
	return nil
}

// flush records the mapping still being extended.
func (o *output) flush() error {
	if o.pending == nil {
		return nil
	}
	pending := *o.pending
	o.pending = nil
	return o.mappings.RecordSourceMapping(pending)
}

func (o *output) Write(p []byte) (int, error) {
	return nativeutility.WriteChunked(o.ctx, o.w, o.maximum, &o.written, p)
}

func (o *output) text(parts ...string) error {
	for _, part := range parts {
		if _, err := io.WriteString(o, part); err != nil {
			return err
		}
	}
	return nil
}
