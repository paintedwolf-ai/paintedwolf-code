package commandsurface

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/projectroot"
)

var (
	// ErrArgvRequired reports a call with neither command nor pipeline.
	ErrArgvRequired = errors.New("missing command or pipeline argument")
	// ErrArgvConflict reports a call that set both command and pipeline.
	ErrArgvConflict = errors.New("command and pipeline are mutually exclusive")
	// ErrPipelineShape reports a pipeline value that is not an array of non-empty strings.
	ErrPipelineShape = errors.New("pipeline must be an array of non-empty strings")
	// ErrDirectoryChangeCommand reports `cd` run as a program.
	ErrDirectoryChangeCommand = errors.New("`cd` changes only its own process; pass cwd")
	// ErrStdinWithSequence reports stdin passed to a sequenced command line.
	ErrStdinWithSequence = errors.New("stdin feeds one program; a sequence runs several")
	// ErrSequenceUnsupported reports sequences on single-process surfaces.
	ErrSequenceUnsupported = errors.New("this surface runs one program; `&&`, `||`, and `;` run several")
	// ErrStdinSources reports literal stdin and stdin_from on one call.
	ErrStdinSources = errors.New("stdin and stdin_from are mutually exclusive")
	// ErrAppendWithoutTarget reports append with neither stdout_to nor stderr_to.
	ErrAppendWithoutTarget = errors.New("append requires stdout_to or stderr_to")
)

// shellBuiltinRedirects maps builtins whose effects cannot escape a child process.
var shellBuiltinRedirects = map[string]error{
	"cd":     ErrDirectoryChangeCommand,
	"export": argv.ErrEnvAssignmentCommand,
}

// Plan is a parsed command and the streams it reads and writes. Parsing never
// mutates the arguments it reads.
type Plan struct {
	Stages []exec.Stage
	IO     IOBinding
}

// IOBinding is the call's explicit stream fields. Inline redirections stay on
// their stages; Plan joins both views.
type IOBinding struct {
	// Stdin is literal input for the plan's first stage.
	Stdin     string
	StdinFrom string
	StdoutTo  string
	StderrTo  string
	// Append applies to stdout_to and stderr_to.
	Append bool
	// Cwd is the caller's working directory; inline paths resolve under it.
	Cwd string
}

// RedirectPath is one inline redirection file.
type RedirectPath struct {
	// Written is the path as the stage names it, relative to the process directory.
	Written string
	// Path is the project path: Written resolved under the call's cwd.
	Path string
}

// InlineWrites lists the files stage redirections write, in plan order.
func (p Plan) InlineWrites() []RedirectPath {
	var out []RedirectPath
	seen := map[string]bool{}
	for _, stage := range p.Stages {
		for _, written := range stage.Streams.WritePaths() {
			if !seen[written] {
				seen[written] = true
				out = append(out, RedirectPath{Written: written, Path: joinCwd(p.IO.Cwd, written)})
			}
		}
	}
	return out
}

// InlineReads lists the files stage redirections read, in plan order.
func (p Plan) InlineReads() []RedirectPath {
	var out []RedirectPath
	seen := map[string]bool{}
	for _, stage := range p.Stages {
		written := stage.Streams.Stdin.Path
		if written != "" && !seen[written] {
			seen[written] = true
			out = append(out, RedirectPath{Written: written, Path: joinCwd(p.IO.Cwd, written)})
		}
	}
	return out
}

// WritePaths names every file the call writes, as project paths.
func (p Plan) WritePaths() []string {
	var out []string
	for _, path := range []string{p.IO.StdoutTo, p.IO.StderrTo} {
		if path != "" {
			out = append(out, path)
		}
	}
	for _, inline := range p.InlineWrites() {
		out = append(out, inline.Path)
	}
	return out
}

// ReadPaths names every file the call reads as a stream, as project paths.
func (p Plan) ReadPaths() []string {
	var out []string
	if p.IO.StdinFrom != "" {
		out = append(out, p.IO.StdinFrom)
	}
	for _, inline := range p.InlineReads() {
		out = append(out, inline.Path)
	}
	return out
}

// ParsePlan normalizes command or pipeline arguments and binds their streams.
func ParsePlan(args map[string]any) (Plan, error) {
	stages, err := parseStages(args)
	if err != nil {
		return Plan{}, err
	}
	io, err := bindIO(stages, args)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Stages: stages, IO: io}, nil
}

// ResolvePlan inserts opaque values into an already parsed command plan.
func ResolvePlan(args map[string]any, substitute func(string) (string, error)) (Plan, error) {
	plan, err := ParsePlan(args)
	if err != nil {
		return Plan{}, err
	}
	for i := range plan.Stages {
		stage := &plan.Stages[i]
		stage.Canonical = stage.CommandLine()
		stage.Name, err = substitute(stage.Name)
		if err != nil {
			return Plan{}, err
		}
		for j := range stage.Args {
			stage.Args[j], err = substitute(stage.Args[j])
			if err != nil {
				return Plan{}, err
			}
		}
		if len(stage.Env) > 0 {
			subEnv := make(map[string]string, len(stage.Env))
			for k, v := range stage.Env {
				subV, err := substitute(v)
				if err != nil {
					return Plan{}, err
				}
				subEnv[k] = subV
			}
			stage.Env = subEnv
		}
	}
	if plan.IO.Stdin, err = substitute(plan.IO.Stdin); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func parseStages(args map[string]any) ([]exec.Stage, error) {
	command, _ := args["command"].(string)
	command = strings.TrimSpace(command)

	pipelineLines, err := pipelineLinesFromArg(args["pipeline"])
	if err != nil {
		return nil, err
	}

	hasPipeline := len(pipelineLines) > 0
	if command != "" && hasPipeline {
		return nil, ErrArgvConflict
	}
	var stages []exec.Stage
	switch {
	case command != "":
		stages, err = exec.StagesFromCommandLine(command)
	case hasPipeline:
		stages, err = exec.StagesFromCommandLines(pipelineLines)
	default:
		return nil, ErrArgvRequired
	}
	if err != nil {
		return nil, err
	}
	stages, err = foldExportStages(stages)
	if err != nil {
		return nil, err
	}
	if err := validatePlan(stages); err != nil {
		return nil, err
	}
	return stages, exec.ValidateStreams(stages)
}

// validatePlan checks every stage for ineffective child-process builtins and blocked env keys.
func validatePlan(stages []exec.Stage) error {
	for _, stage := range stages {
		if err, found := shellBuiltinRedirects[strings.ToLower(strings.TrimSpace(stage.Name))]; found {
			return err
		}
		if len(stage.Env) > 0 {
			if err := exec.ValidateInlineEnv(stage.Env); err != nil {
				return err
			}
		}
	}
	return nil
}

// bindIO reads the explicit stream fields and refuses a stream named twice.
func bindIO(stages []exec.Stage, args map[string]any) (IOBinding, error) {
	io := IOBinding{
		Stdin:     stringArg(args, "stdin"),
		StdinFrom: strings.TrimSpace(stringArg(args, "stdin_from")),
		StdoutTo:  strings.TrimSpace(stringArg(args, "stdout_to")),
		StderrTo:  strings.TrimSpace(stringArg(args, "stderr_to")),
		Cwd:       strings.TrimSpace(stringArg(args, "cwd")),
	}
	io.Append, _ = args["append"].(bool)
	if io.Stdin != "" && io.StdinFrom != "" {
		return io, ErrStdinSources
	}
	if io.Append && io.StdoutTo == "" && io.StderrTo == "" {
		return io, ErrAppendWithoutTarget
	}
	if (io.Stdin != "" || io.StdinFrom != "") && sequencesPrograms(stages) {
		return io, ErrStdinWithSequence
	}
	for i, stage := range stages {
		streams := stage.Streams
		if io.StdoutTo != "" && writesFile(streams.StdoutSink()) {
			return io, &argv.RedirectionError{Issue: argv.IssueStdoutConflict, Operator: stage.CommandLine()}
		}
		if io.StderrTo != "" && writesFile(streams.StderrSink()) {
			return io, &argv.RedirectionError{Issue: argv.IssueStderrConflict, Operator: stage.CommandLine()}
		}
		if i == 0 && (io.Stdin != "" || io.StdinFrom != "") && streams.Stdin != (argv.Source{}) {
			return io, &argv.RedirectionError{Issue: argv.IssueStdinConflict, Operator: stage.CommandLine()}
		}
	}
	return io, nil
}

func writesFile(sink argv.Sink) bool { return sink.Kind == argv.SinkFile }

func stringArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// foldExportStages merges sequential export assignments into following command stages.
func foldExportStages(stages []exec.Stage) ([]exec.Stage, error) {
	if len(stages) == 0 {
		return stages, nil
	}
	hasExport := false
	for _, s := range stages {
		if strings.ToLower(strings.TrimSpace(s.Name)) == "export" {
			hasExport = true
			break
		}
	}
	if !hasExport {
		return stages, nil
	}

	var out []exec.Stage
	accumulatedEnv := make(map[string]string)

	for i, stage := range stages {
		if strings.ToLower(strings.TrimSpace(stage.Name)) == "export" {
			canFold := len(stage.Args) > 0 || len(stage.Env) > 0
			for _, arg := range stage.Args {
				k, v, ok := strings.Cut(arg, "=")
				if !ok || !argv.IsEnvIdentifier(k) {
					canFold = false
					break
				}
				accumulatedEnv[k] = v
			}
			for k, v := range stage.Env {
				accumulatedEnv[k] = v
			}
			if !canFold || i == len(stages)-1 {
				out = append(out, stage)
				continue
			}
			continue
		}

		if len(accumulatedEnv) > 0 {
			if stage.Env == nil {
				stage.Env = make(map[string]string, len(accumulatedEnv))
			}
			for k, v := range accumulatedEnv {
				if _, exists := stage.Env[k]; !exists {
					stage.Env[k] = v
				}
			}
			accumulatedEnv = make(map[string]string)
		}
		out = append(out, stage)
	}
	if len(out) > 0 && out[0].Connector != argv.ConnectorNone {
		out[0].Connector = argv.ConnectorNone
	}
	return out, nil
}

func sequencesPrograms(stages []exec.Stage) bool {
	for _, stage := range stages {
		if stage.Connector.Sequencing() {
			return true
		}
	}
	return false
}

// pipelineLinesFromArg accepts wire and in-process pipeline values.
func pipelineLinesFromArg(raw any) ([]string, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return pipelineLinesFromAny(out)
	case []any:
		return pipelineLinesFromAny(v)
	default:
		return nil, ErrPipelineShape
	}
}

func pipelineLinesFromAny(raw []any) ([]string, error) {
	var lines []string
	for i, item := range raw {
		line, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("pipeline[%d] must be a string: %w", i, ErrPipelineShape)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return nil, fmt.Errorf("pipeline[%d] is empty: %w", i, ErrPipelineShape)
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// PlanGroups returns each process group's stage lines from the executor's parse.
func PlanGroups(args map[string]any) ([][]string, bool) {
	stages, err := parseStages(args)
	if err != nil || len(stages) == 0 {
		return nil, false
	}
	groups := exec.GroupStages(stages)
	plan := make([][]string, 0, len(groups))
	for _, group := range groups {
		lines := make([]string, 0, group.Len())
		for i := group.Start; i < group.End; i++ {
			lines = append(lines, stages[i].CommandLine())
		}
		plan = append(plan, lines)
	}
	return plan, true
}

// PrimaryCommandLine returns the user-visible command line for reject hints.
func PrimaryCommandLine(args map[string]any, stages []exec.Stage) string {
	if cmd, _ := args["command"].(string); strings.TrimSpace(cmd) != "" {
		return strings.TrimSpace(cmd)
	}
	if lines, err := pipelineLinesFromArg(args["pipeline"]); err == nil && len(lines) > 0 {
		return strings.Join(lines, " | ")
	}
	if len(stages) > 0 {
		return stages[0].CommandLine()
	}
	return ""
}

// RenderStages writes a plan's stages back as one command line that parses to them.
func RenderStages(stages []exec.Stage) string {
	var b strings.Builder
	for i, stage := range stages {
		if i > 0 {
			b.WriteString(" " + string(stage.Connector.OrPipe()) + " ")
		}
		b.WriteString(stage.CommandLine())
	}
	return b.String()
}

func joinCwd(cwd, p string) string {
	if cwd == "" || cwd == "." || strings.HasPrefix(p, "/") {
		return p
	}
	if _, scratch := projectroot.ScratchAddress(p); scratch {
		return p
	}
	return strings.TrimSuffix(cwd, "/") + "/" + p
}
