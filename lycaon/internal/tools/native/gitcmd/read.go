package gitcmd

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// command is one bound Git invocation: its global -C directories and
// allowlisted -c overrides, and the subcommand argv Git receives.
type command struct {
	spec                  toolcontract.CommandSpecification
	dirs                  []string
	config                []string
	args                  []string
	beforeDash, afterDash []string
}

// allowedConfig are the -c overrides a command may set, keyed by lowercase
// name: each narrows what Git runs or how it prints, or names the identity a
// commit records or the branch a new repository starts on.
var allowedConfig = map[string]func(string) bool{
	"core.fsmonitor":     oneOf("false"),
	"core.pager":         oneOf("cat"),
	"color.ui":           oneOf("false", "never", "auto"),
	"core.quotepath":     oneOf("true", "false", "on", "off", "yes", "no", "1", "0"),
	"user.name":          nonEmpty,
	"user.email":         nonEmpty,
	"init.defaultbranch": nonEmpty,
}

func oneOf(values ...string) func(string) bool {
	return func(value string) bool {
		for _, allowed := range values {
			if strings.EqualFold(value, allowed) {
				return true
			}
		}
		return false
	}
}

func nonEmpty(value string) bool { return value != "" }

// configValue is the last -c override for key, as Git applies them in order.
func (c *command) configValue(key string) string {
	value := ""
	for _, kv := range c.config {
		if k, v, _ := strings.Cut(kv, "="); strings.EqualFold(k, key) {
			value = v
		}
	}
	return value
}

// parseCommand splits the bound argv into globals and the subcommand argv.
// The binder has admitted every word, so Git receives the words as written
// after the subcommand; only global options are taken apart.
func parseCommand(spec toolcontract.CommandSpecification, argv commandprogram.Fields, bound commandprogram.BoundArgv) (*command, error) {
	names := strings.Fields(spec.Name)
	c := &command{spec: spec}
	i := 1
	for i < len(argv) && argv[i].Value != names[1] {
		word := argv[i].Value
		switch {
		case word == "-C" || word == "-c":
			if i+1 >= len(argv) {
				return nil, operandInvalid(spec, word, "a value for "+word)
			}
			c.addGlobal(word, argv[i+1].Value)
			i += 2
		case strings.HasPrefix(word, "-C") || strings.HasPrefix(word, "-c"):
			c.addGlobal(word[:2], word[2:])
			i++
		case word == "--no-pager":
			i++
		default:
			return nil, operandInvalid(spec, word, "a global option before "+names[1])
		}
	}
	i += len(names) - 1
	if i > len(argv) {
		return nil, operandInvalid(spec, spec.Name, "the "+spec.Name+" subcommand")
	}
	for _, kv := range c.config {
		key, value, _ := strings.Cut(kv, "=")
		if allowed := allowedConfig[strings.ToLower(key)]; allowed == nil || !allowed(value) {
			return nil, &toolrejection.ToolReject{Code: "COMMAND_OPTION_UNSUPPORTED", Data: map[string]any{
				"command": spec.Name, "word": kv, "expected": "an allowlisted -c override (user.name, user.email, init.defaultBranch, core.fsmonitor=false, core.pager=cat, color.ui=false, core.quotepath=false)", "expected_form": spec.Argv}}
		}
	}
	c.args = normalizeAttachedGitOptions(spec.Name, append(append([]string{}, names[1:]...), argv[i:].Values()...))
	c.beforeDash, c.afterDash = splitDashOperands(spec, c.args, bound)
	rule := readForms[spec.Name]
	if rule == nil {
		rule = mutateForms[spec.Name]
	}
	if err := rule(spec, bound); err != nil {
		return nil, err
	}
	if err := formatPlaceholders(spec, bound); err != nil {
		return nil, err
	}
	if spec.Name == "git shortlog" && len(c.beforeDash) == 0 {
		// Without a revision Git would read a log from standard input.
		c.args = withDefaultRevision(c.args, "HEAD")
	}
	if spec.Name == "git config" {
		// Repository configuration only: the hermetic invocation's own
		// overrides are not the user's configuration.
		c.args = configReadArgs(bound)
	}

	return c, nil
}

func (c *command) addGlobal(flag, value string) {
	if flag == "-C" {
		c.dirs = append(c.dirs, value)
		return
	}
	c.config = append(c.config, value)
}

func normalizeAttachedGitOptions(name string, args []string) []string {
	if name != "git diff" && name != "git status" {
		return args
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			out = append(out, args[i:]...)
			break
		}
		if i+1 < len(args) && args[i+1] != "--" {
			switch {
			case name == "git diff" && args[i] == "-U":
				out = append(out, "-U"+args[i+1])
				i++
				continue
			case name == "git diff" && args[i] == "--unified":
				out = append(out, "--unified="+args[i+1])
				i++
				continue
			case name == "git status" && args[i] == "--untracked-files":
				out = append(out, "--untracked-files="+args[i+1])
				i++
				continue
			}
		}
		out = append(out, args[i])
	}
	return out
}

// operandRule refuses operand shapes that would make a read form write or
// reach the network.
type operandRule func(toolcontract.CommandSpecification, commandprogram.BoundArgv) error

func anyOperands(toolcontract.CommandSpecification, commandprogram.BoundArgv) error { return nil }

func noOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if operands := bound.Operands(); len(operands) > 0 {
		return operandInvalid(spec, operands[0].Value, "no operands")
	}
	return nil
}

func blameOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if len(bound.Operands()) == 0 {
		return operandInvalid(spec, spec.Name, "a PATH")
	}
	return nil
}

// branchOperands: names are patterns only with --list; otherwise Git
// would create a branch.
func branchOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if operands := bound.Operands(); len(operands) > 0 && !has(bound, "--list") {
		return operandInvalid(spec, operands[0].Value, "--list before a branch PATTERN")
	}
	return nil
}

// tagOperands: tag lists only with -l/--list.
func tagOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if !has(bound, "-l") {
		return operandInvalid(spec, spec.Name, "-l or --list")
	}
	return nil
}

func revListOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if len(bound.Operands()) == 0 {
		return operandInvalid(spec, spec.Name, "a REF_RANGE")
	}
	return nil
}

func mergeBaseOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if operands := bound.Operands(); len(operands) != 2 {
		return operandInvalid(spec, spec.Name, "two REFs")
	}
	return nil
}

// remoteOperands admits listing and get-url; remote show and every other
// subcommand either writes or contacts the remote.
func remoteOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	operands := bound.Operands().Values()
	switch {
	case len(operands) == 0:
		return nil
	case len(operands) == 2 && operands[0] == "get-url" && !has(bound, "-v"):
		return nil
	}
	return operandInvalid(spec, operands[0], "-v or get-url NAME")
}

// reflogOperands admits the listing: an optional show and at most one REF.
func reflogOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	operands := bound.Operands().Values()
	if len(operands) > 0 && operands[0] == "show" {
		operands = operands[1:]
	}
	if len(operands) > 1 {
		return operandInvalid(spec, operands[0], "show and at most one REF")
	}
	if len(operands) == 1 && reflogSubcommands[operands[0]] {
		return operandInvalid(spec, operands[0], "show and at most one REF")
	}
	return nil
}

// reflogSubcommands change or inspect reflogs other than by listing.
var reflogSubcommands = map[string]bool{"expire": true, "delete": true, "drop": true, "exists": true, "list": true}

func lsTreeOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	if len(bound.Operands()) == 0 {
		return operandInvalid(spec, spec.Name, "a tree-ish REV")
	}
	return nil
}

// catFileOperands takes exactly one query and one object; batch modes are
// not catalogued, so one call answers one object.
func catFileOperands(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	queries := 0
	for _, key := range []string{"-t", "-s", "-p", "-e"} {
		if has(bound, key) {
			queries++
		}
	}
	if queries != 1 || len(bound.Operands()) != 1 {
		return operandInvalid(spec, spec.Name, "exactly one of -t, -s, -p or -e and one OBJECT")
	}
	return nil
}

// formatPlaceholders refuses signature placeholders, which make Git run a
// verification program.
func formatPlaceholders(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) error {
	for _, option := range bound.Options("--format") {
		for _, value := range option.Values {
			if strings.Contains(value.Value, "%G") || strings.Contains(value.Value, "%(signature") {
				return operandInvalid(spec, value.Value, "a format without signature placeholders")
			}
		}
	}
	return nil
}

func has(bound commandprogram.BoundArgv, key string) bool {
	_, ok := bound.Last(key)
	return ok
}

func operandInvalid(spec toolcontract.CommandSpecification, word, expected string) error {
	return &toolrejection.ToolReject{Code: "COMMAND_OPERAND_INVALID", Data: map[string]any{
		"command": spec.Name, "word": word, "expected": expected, "reason": expected, "expected_form": spec.Argv}}
}

// run resolves the directory and repository, then streams Git's output.
// Git's own fatal messages and status 128 stand for a missing -C directory
// or a directory outside any repository.
func (p Prepared) run(ctx context.Context, streams commandprogram.Streams) (int, error) {
	stderr := streams.Error
	if stderr == nil {
		stderr = io.Discard
	}
	dir, _, status, err := resolveCommandRepo(ctx, p, stderr, false)
	if err != nil || status != 0 {
		return status, err
	}
	out := &meteredOutput{ctx: ctx, w: streams.Output, maximum: p.owners.limit("stream_bytes")}
	if out.w == nil {
		out.w = io.Discard
	}
	diagnostics, status, err := (git.CommandReader{}).ReadCommand(ctx, dir, p.command.args, p.command.config, out)
	if out.err != nil {
		return 0, out.err
	}
	if err != nil {
		var coded interface{ Code() string }
		if errors.As(err, &coded) {
			return 0, errors.Join(err, &toolrejection.ToolReject{Code: coded.Code(), Data: map[string]any{"command": p.command.spec.Name}})
		}
		return 0, err
	}
	if _, werr := stderr.Write(diagnostics); werr != nil {
		return 0, werr
	}
	return status, nil
}

// meteredOutput bounds stdout by stream_bytes and remembers the refusal, so
// a cut stream is never reported as Git's own failure.
type meteredOutput struct {
	ctx              context.Context
	w                io.Writer
	maximum, written int64
	err              error
}

func (o *meteredOutput) Write(p []byte) (int, error) {
	if o.err != nil {
		return 0, o.err
	}
	if err := o.ctx.Err(); err != nil {
		o.err = err
		return 0, err
	}
	if o.maximum > 0 && o.written+int64(len(p)) > o.maximum {
		o.err = &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": o.maximum, "observed": o.written + int64(len(p))}}
		return 0, o.err
	}
	n, err := o.w.Write(p)
	o.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		o.err = err
	}
	return n, err
}

func withDefaultRevision(args []string, rev string) []string {
	for i, arg := range args {
		if arg == "--" {
			return append(append(append([]string{}, args[:i]...), rev), args[i:]...)
		}
	}
	return append(args, rev)
}
