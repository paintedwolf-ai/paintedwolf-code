package textutil

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/benhoyt/goawk/interp"
	"github.com/benhoyt/goawk/parser"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// awkInterpreterBytes is the program working memory held while GoAWK runs;
// values it creates are metered separately by the work budget.
const awkInterpreterBytes = 1 << 20

// runAwk runs one program on the vendored GoAWK v1.32.0 interpreter. The
// program is parsed and inspected before any input is read: system(),
// command pipes in either direction, getline from a file, output redirection
// and regex literals using RE2-only syntax refuse. Execution has no
// environment, no command execution and no file writes; PATH operands are
// served from their pinned captures, so FILENAME and FNR keep their names.
// Regexes are Go RE2 syntax with GoAWK's leftmost-longest matching, limited
// at parse to the POSIX ERE subset for literals. Backward jumps, for-in
// iterations and function calls count against query_work_steps, and created
// strings and array elements against evaluation_memory_bytes.
func runAwk(inv *invocation) error {
	programWord, text, operands, err := awkProgram(inv)
	if err != nil {
		return err
	}
	vars, err := awkVars(inv)
	if err != nil {
		return err
	}
	program, err := parser.ParseProgram([]byte(text), nil)
	if err != nil {
		return errors.Join(err, inv.usage(programWord, "a valid AWK program: "+err.Error()))
	}
	if err := awkInspect(inv, programWord, program); err != nil {
		return err
	}
	paths, err := newPaths(inv)
	if err != nil {
		return err
	}
	files := &pinnedFiles{inv: inv, paths: paths, operands: make(map[string]commandprogram.Field)}
	defer files.Close()
	var args []string
	for _, operand := range operands {
		args = append(args, operand.Value)
		if operand.Value != "-" {
			files.operands[fsKey(operand.Value)] = operand
		}
	}
	if err := inv.reserve(awkInterpreterBytes + 3*inv.limit("record_bytes")); err != nil {
		return err
	}
	interpreter, err := interp.New(program)
	if err != nil {
		return err
	}
	budget := &awkBudget{steps: inv.limit("query_work_steps"), bytes: inv.limit("evaluation_memory_bytes"), memory: inv.ports.Memory, ctx: inv.ctx}
	defer budget.Close()
	status, err := interpreter.ExecuteContext(inv.ctx, &interp.Config{
		Stdin: inv.streams.Input, Output: inv.out, Error: &inv.errors,
		Argv0: "awk", Args: args, NoArgVars: true, Vars: vars,
		NoExec: true, NoFileWrites: true, FileSystem: files,
		Environ: []string{}, Budget: budget, MaxInputRecord: int(inv.limit("record_bytes")),
	})
	if files.failure != nil {
		return files.failure
	}
	var missing *awkInputFailure
	if errors.As(err, &missing) {
		fmt.Fprintf(&inv.errors, "awk: fatal: cannot open file '%s' for reading: %s\n", missing.name, missing.reason)
		inv.status = 2
		return nil
	}
	var record *interp.RecordLimitError
	if errors.As(err, &record) {
		return tools.CommandLimitExceeded("record_bytes", int64(record.Maximum), int64(record.Observed))
	}
	var limit *awkLimit
	switch {
	case errors.As(err, &limit):
		return tools.CommandLimitExceeded(limit.id, limit.maximum, limit.maximum+1)
	case inv.ctx.Err() != nil:
		return inv.ctx.Err()
	case err != nil:
		var runtime *interp.Error
		if errors.As(err, &runtime) {
			inv.errors.WriteString("awk: " + runtime.Error() + "\n")
			inv.status = 2
			return nil
		}
		return err
	}
	inv.status = max(inv.status, status)
	return nil
}

// awkVars binds -F to FS and each -v NAME=VALUE.
func awkVars(inv *invocation) ([]string, error) {
	var vars []string
	if sep, ok := inv.value("-F"); ok {
		vars = append(vars, "FS", sep.Value)
	}
	for _, option := range inv.argv.Options("-v") {
		word := option.Values[0]
		name, value, ok := strings.Cut(word.Value, "=")
		if !ok || !commandprogram.VariableName(name) {
			return nil, inv.usage(word, "NAME=VALUE with an AWK variable name")
		}
		vars = append(vars, name, value)
	}
	return vars, nil
}

// awkInspect refuses constructs outside the catalog before execution.
func awkInspect(inv *invocation, word commandprogram.Field, program *parser.Program) error {
	for _, construct := range program.Constructs() {
		expected := ""
		switch construct.Kind {
		case "system":
			expected = "no system()"
		case "output-pipe", "input-pipe":
			expected = "no command pipes"
		case "getline-file":
			expected = "getline from standard input or the current record only"
		case "output-file":
			expected = "output to standard output; redirect the command instead"
		case "regex":
			if bad := re2OnlySyntax(construct.Regex); bad != "" {
				expected = "POSIX ERE regex literals; " + bad + " is RE2 syntax"
			}
		}
		if expected != "" {
			return toolrejection.RejectInvalidArguments("COMMAND_NATIVE_FORM_UNSUPPORTED", map[string]any{
				"command": "awk", "word": word.Value, "expected": expected, "expected_form": inv.spec.Argv,
				"program_line": construct.Line, "program_column": construct.Column,
				"source_start_byte": word.Span.StartByte, "source_end_byte": word.Span.EndByte, "source_line": word.Span.Line, "source_column": word.Span.Column,
			})
		}
	}
	return nil
}

// re2OnlySyntax names the first construct of a regex literal that Go's RE2
// grammar accepts but POSIX ERE does not define.
func re2OnlySyntax(regex string) string {
	inBracket := false
	for i := 0; i < len(regex); i++ {
		c := regex[i]
		switch {
		case inBracket:
			if c == ']' {
				inBracket = false
			}
		case c == '[':
			inBracket = true
			if i+1 < len(regex) && regex[i+1] == ']' {
				i++
			}
		case c == '\\' && i+1 < len(regex):
			i++
			if strings.IndexByte("dDwWsSbBAzpPQECx", regex[i]) >= 0 {
				return `\` + string(regex[i])
			}
		case c == '(' && i+1 < len(regex) && regex[i+1] == '?':
			return "(?"
		case (c == '*' || c == '+' || c == '?' || c == '}') && i+1 < len(regex) && regex[i+1] == '?':
			return string(c) + "?"
		}
	}
	return ""
}

// awkLimit is the budget's stop, mapped to the catalog limit it names.
type awkLimit struct {
	id      string
	maximum int64
}

func (l *awkLimit) Error() string { return "awk exceeded " + l.id }

type awkBudget struct {
	ctx          context.Context
	memory       *toolcontract.ProgramMemory
	steps, bytes int64
	used, made   int64
}

func (b *awkBudget) Step() error {
	if b.used++; b.used > b.steps {
		return &awkLimit{id: "query_work_steps", maximum: b.steps}
	}
	return nil
}

func (b *awkBudget) Allocate(n int) error {
	if n < 0 || int64(n) > b.bytes-b.made {
		return &awkLimit{id: "evaluation_memory_bytes", maximum: b.bytes}
	}
	if b.memory != nil {
		if err := b.memory.ReserveBytes(b.ctx, int64(n)); err != nil {
			return err
		}
	}
	b.made += int64(n)
	return nil
}

func (b *awkBudget) Retain(n int) error {
	if int64(n) >= b.made {
		return b.Allocate(n - int(b.made))
	}
	if b.memory != nil {
		b.memory.ReleaseBytes(b.made - int64(n))
	}
	b.made = int64(n)
	return nil
}
func (b *awkBudget) Close() {
	if b.memory != nil {
		b.memory.ReleaseBytes(b.made)
	}
	b.made = 0
}

// pinnedFiles opens the current AWK operand through the shared capture owner.
// GoAWK consumes ordinary input files sequentially; no operand body is retained.
type pinnedFiles struct {
	inv      *invocation
	paths    *projectpaths.ProgramPaths
	operands map[string]commandprogram.Field
	current  io.ReadCloser
	failure  error
}

func (f *pinnedFiles) Close() {
	if f.current != nil {
		_ = f.current.Close()
		f.current = nil
	}
}
func (f *pinnedFiles) Open(name string) (fs.File, error) {
	operand, ok := f.operands[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	f.Close()
	reader, revision, err := f.inv.capture(f.paths, operand)
	if err != nil {
		if reason, ordinaryFailure := ordinary(err); ordinaryFailure {
			return nil, &awkInputFailure{name: operand.Value, reason: reason}
		}
		f.failure = pathRejection(err, operand)
		return nil, err
	}
	f.current = reader
	var size int64
	if revision != nil {
		size = revision.ContentBytes
	}
	return &pinnedFile{ReadCloser: reader, name: name, size: size}, nil
}

// fsKey spells a name the way GoAWK presents it to its file system.
func fsKey(name string) string {
	if cleaned := path.Clean(name); fs.ValidPath(cleaned) {
		return cleaned
	}
	return name
}

type pinnedFile struct {
	io.ReadCloser
	name string
	size int64
}

func (f *pinnedFile) Stat() (fs.FileInfo, error) { return pinnedInfo{f}, nil }

type pinnedInfo struct{ f *pinnedFile }

func (i pinnedInfo) Name() string       { return path.Base(i.f.name) }
func (i pinnedInfo) Size() int64        { return i.f.size }
func (i pinnedInfo) Mode() fs.FileMode  { return 0o444 }
func (i pinnedInfo) ModTime() time.Time { return time.Time{} }
func (i pinnedInfo) IsDir() bool        { return false }
func (i pinnedInfo) Sys() any           { return nil }

// awkProgram is the -f files joined with newlines in order, as every awk
// reads them, or else the first operand; the rest are inputs.
func awkProgram(inv *invocation) (commandprogram.Field, string, commandprogram.Fields, error) {
	operands := inv.operands()
	files := inv.argv.Options("-f")
	if len(files) == 0 {
		if len(operands) == 0 {
			return commandprogram.Field{}, "", nil, inv.usage(inv.commandWord(), "a PROGRAM")
		}
		return operands[0], operands[0].Value, operands[1:], nil
	}
	parts := make([]string, len(files))
	for i, option := range files {
		text, err := readScriptFile(inv, option.Values[0])
		if err != nil {
			return commandprogram.Field{}, "", nil, err
		}
		parts[i] = text
	}
	return files[0].Values[0], strings.Join(parts, "\n"), operands, nil
}

type awkInputFailure struct{ name, reason string }

func (e *awkInputFailure) Error() string { return e.name + ": " + e.reason }
