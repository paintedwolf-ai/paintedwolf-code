package projectpaths

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// PathError carries a catalog diagnostic without deriving control from its text.
type PathError struct {
	Code         string
	Operand      string
	Cause        error
	LimitID      string
	Maximum      int64
	Observed     int64
	Command      string
	ExpectedForm string
	ResourceUnit string
}

func (e *PathError) Error() string { return fmt.Sprintf("%s: path %q: %v", e.Code, e.Operand, e.Cause) }
func (e *PathError) Unwrap() error { return e.Cause }

// BoundPath keeps root identity separate from native and display spellings.
// Resolved contains addressed coordinates; Open and MutationTarget establish
// reached identity at the effect boundary. Conversion grants no authority.
type BoundPath struct {
	Resolved      Resolved
	Null          bool
	rootIdentity  string
	rootInfo      os.FileInfo
	exactRelative string
	admitRead     func(string) error
	admitMutation func(string) (Resolved, error)
}

// Validate rejects a root replaced since binding. Descriptor-relative effect
// operations still recheck every component beneath this directory.
func (p BoundPath) Validate() error {
	if p.Null {
		return nil
	}
	identity, err := directoryIdentity(p.Resolved.Root.Path)
	if err != nil {
		return err
	}
	if identity != p.rootIdentity {
		return &PathError{Code: "COMMAND_OPERAND_INVALID", Operand: p.Resolved.DisplayPath, Cause: errors.New("bound root identity changed")}
	}
	return nil
}

// Open reads through the existing confined filesystem door, never an argv path.
func (p BoundPath) Open() (*os.File, error) {
	if p.Null {
		return nil, &PathError{Code: "COMMAND_OPERAND_INVALID", Cause: errors.New("null input is an empty stream, not a file")}
	}
	root, err := p.openRoot()
	if err != nil {
		return nil, pathOperationError(p.Resolved.DisplayPath, err)
	}
	defer root.Close()
	file, err := root.OpenExact(p.exactRelative)
	if err != nil {
		return nil, pathOperationError(p.Resolved.DisplayPath, err)
	}
	if p.admitRead != nil {
		if err := p.admitRead(file.Name()); err != nil {
			file.Close()
			return nil, pathOperationError(p.Resolved.DisplayPath, err)
		}
	}
	return file, nil
}

// ProgramPaths is one invocation's logical cwd. It never changes process cwd,
// mutates grants, or rewrites external argv. Each invocation has a separate value.
type ProgramPaths struct {
	boundary      *sandbox.Boundary
	context       tools.ToolContext
	cwd           BoundPath
	initial       BoundPath
	initialInfo   os.FileInfo
	forms         map[string]string
	cwdInfo       os.FileInfo
	argumentLimit int64
	outputLimit   int64
}

// NewProgramPaths binds the invocation directory through current path authority.
func NewProgramPaths(ctx context.Context, boundary *sandbox.Boundary, tc tools.ToolContext, cwd string) (*ProgramPaths, error) {
	catalog, err := toolcontract.LanguageV1()
	if err != nil {
		return nil, err
	}
	tc.Source.Roots = append([]projectroot.RootRef(nil), tc.Source.Roots...)
	p := &ProgramPaths{boundary: boundary, context: tc, forms: map[string]string{}}
	for _, command := range catalog.Commands {
		p.forms[command.Name] = command.Argv
	}
	for _, limit := range catalog.Language.Limits {
		if limit.ID == "argument_bytes" {
			p.argumentLimit = limit.Maximum
		}
		if limit.ID == "stream_bytes" {
			p.outputLimit = limit.Maximum
		}
	}
	if p.argumentLimit <= 0 || p.outputLimit <= 0 {
		return nil, errors.New("path limits missing from language contract")
	}
	if cwd == "" {
		cwd = "."
	}
	bound, err := p.Bind(ctx, cwd, false)
	if err != nil {
		return nil, err
	}
	bound, info, err := p.directoryBinding(ctx, bound)
	if err != nil {
		return nil, err
	}
	p.cwdInfo = info
	p.cwd = bound
	p.initial = bound
	p.initialInfo = info
	return p, nil
}

// Bind resolves one exact native operand. A write binding checks mutation scope
// but does not record a mutation before a complete operation actually runs.
func (p *ProgramPaths) Bind(ctx context.Context, operand string, write bool) (BoundPath, error) {
	if err := ctx.Err(); err != nil {
		return BoundPath{}, err
	}
	if int64(len(operand)) > p.argumentLimit {
		return BoundPath{}, pathLimit("argument_bytes", "bytes", p.argumentLimit, int64(len(operand)))
	}
	if err := ValidateNativeOperand(operand); err != nil {
		return BoundPath{}, err
	}
	if IsNullOperand(operand) {
		return BoundPath{Null: true}, nil
	}
	addressed := operand
	if p.cwd.Resolved.Abs != "" {
		held, err := p.OpenCwd(ctx)
		if err != nil {
			return BoundPath{}, err
		}
		held.Close()
		if !filepath.IsAbs(operand) && !strings.HasPrefix(operand, "@") {
			addressed = p.cwd.Resolved.Abs + string(filepath.Separator) + operand
		}
	}
	var resolved Resolved
	var err error
	if write {
		resolved, err = resolveMutationScope(ctx, p.boundary, p.context, p.rootAddress(addressed))
	} else {
		resolved, err = ResolveRead(ctx, nil, p.context, p.rootAddress(addressed))
	}
	if err != nil {
		return BoundPath{}, err
	}
	identity, err := directoryIdentity(resolved.Root.Path)
	if err != nil {
		return BoundPath{}, err
	}
	relative, err := exactRelativeOperand(resolved, addressed)
	if err != nil {
		return BoundPath{}, err
	}
	for len(relative) > 0 && os.IsPathSeparator(relative[0]) {
		relative = relative[1:]
	}
	for strings.HasPrefix(relative, "."+string(filepath.Separator)) {
		relative = relative[2:]
	}
	resolved.Abs = resolved.Root.Path
	if relative != "." && relative != "" {
		resolved.Abs += string(filepath.Separator) + relative
	}
	rootInfo, err := os.Stat(resolved.Root.Path + string(filepath.Separator) + ".")
	if err != nil {
		return BoundPath{}, err
	}
	bound := BoundPath{Resolved: resolved, rootIdentity: identity, rootInfo: rootInfo, exactRelative: relative}
	bound.admitRead = func(actual string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		authority, err := authorityAddress(resolved.Root.Path, actual)
		if err != nil {
			return err
		}
		_, err = ResolveRead(ctx, p.boundary, p.context, authority)
		return err
	}
	if write {
		bound.admitMutation = func(actual string) (Resolved, error) {
			if err := ctx.Err(); err != nil {
				return Resolved{}, err
			}
			authority, err := authorityAddress(resolved.Root.Path, actual)
			if err != nil {
				return Resolved{}, err
			}
			return resolveMutationScope(ctx, p.boundary, p.context, authority)
		}
	}
	return bound, nil
}

// Chdir changes only this invocation's cwd after descriptor-relative inspection.
func (p *ProgramPaths) Chdir(ctx context.Context, operand string) error {
	bound, err := p.Bind(ctx, operand, false)
	if err != nil {
		return err
	}
	bound, info, err := p.directoryBinding(ctx, bound)
	if err != nil {
		return err
	}
	p.cwdInfo = info
	p.cwd = bound
	return nil
}

// Cwd reports both the addressed root and native directory without a global cwd.
func (p *ProgramPaths) Cwd() Resolved { return p.cwd.Resolved }

func directoryInfo(path BoundPath) (os.FileInfo, error) {
	file, err := path.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &PathError{Code: "COMMAND_OPERAND_INVALID", Operand: path.Resolved.DisplayPath, Cause: errors.New("directory required")}
	}
	return info, nil
}
func directoryIdentity(path string) (string, error) {
	return fspath.EntryIdentity(path + string(filepath.Separator) + ".")
}
func readRelative(path string) string {
	if path == "" {
		return "."
	}
	return filepath.FromSlash(path)
}

// NativePaths validates exact conversion operands before streaming bounded
// chunks. It grants no authority and never materializes a complete path list.
func (p *ProgramPaths) NativePaths(ctx context.Context, operands []string, nul bool, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var total int64
	for _, operand := range operands {
		native, err := p.nativePath(ctx, operand, nul)
		if err != nil {
			return err
		}
		total += int64(len(native)) + 1
		if total > p.outputLimit {
			return pathLimit("stream_bytes", "bytes", p.outputLimit, total)
		}
	}
	writer := newPathOutput(ctx, output, p.outputLimit)
	delimiter := "\n"
	if nul {
		delimiter = "\x00"
	}
	for _, operand := range operands {
		native, err := p.nativePath(ctx, operand, nul)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(writer, native); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, delimiter); err != nil {
			return err
		}
	}
	return nil
}

func (p *ProgramPaths) nativePath(ctx context.Context, operand string, nul bool) (string, error) {
	bound, err := p.Bind(ctx, operand, false)
	if err != nil {
		return "", err
	}
	if bound.Null {
		return "", invalidOperand(operand, "null device has no ordinary path identity")
	}
	native := bound.Resolved.Abs
	if !nul && strings.HasSuffix(native, "\n") {
		return "", invalidOperand(operand, "native path ending LF is not representable in scalar substitution")
	}
	if err := validateOutputPath(native, nul); err != nil {
		return "", err
	}
	return native, nil
}

func pathLimit(id, unit string, maximum, observed int64) error {
	return &PathError{Code: "COMMAND_LIMIT_EXCEEDED", LimitID: id, ResourceUnit: unit, Maximum: maximum, Observed: observed, Cause: errors.New("path resource limit exceeded")}
}

// OpenReader exposes a typed null input as EOF, retaining ordinary file opens.
func (p BoundPath) OpenReader() (io.ReadCloser, error) {
	if p.Null {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return p.Open()
}

// TemporaryDirectory is the same real session root supplied to child processes.
// Creation of individual temporary files is a separate filesystem operation.
func (p *ProgramPaths) TemporaryDirectory(ctx context.Context) (string, error) {
	if p.context.Host.SessionScratchDir == "" {
		return "", tools.ErrSessionScratchUnavailable
	}
	bound, err := p.Bind(ctx, "@scratch", false)
	if err != nil {
		return "", err
	}
	if _, err = directoryInfo(bound); err != nil {
		return "", err
	}
	return bound.Resolved.Abs, nil
}
