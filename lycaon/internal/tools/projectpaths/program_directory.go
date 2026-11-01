package projectpaths

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// Directory is the frame's absolute logical working directory: the value
// pwd prints, PWD holds and an external process starts in.
func (p *ProgramPaths) Directory() string { return filepath.Clean(p.cwd.Resolved.Abs) }

// PreviousDirectory is OLDPWD: the directory the last successful cd left.
func (p *ProgramPaths) PreviousDirectory() (string, bool) {
	if !p.hasPrevious {
		return "", false
	}
	return filepath.Clean(p.previous.Resolved.Abs), true
}

// Address converts an unquoted @label or @scratch word prefix to its native
// absolute directory. It grants nothing: the operation or launch that uses
// the path checks authority as for any absolute path. A label no attached
// root carries stays literal text (ok false), so `@scope/package` reaches a
// program unchanged; a label a root change retired refuses as stale.
func (p *ProgramPaths) Address(ctx context.Context, label string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	resolved, err := ResolveRead(ctx, nil, p.context, "@"+label)
	if err == nil {
		bound, err := p.Bind(ctx, resolved.Abs, false)
		if err != nil {
			return "", false, err
		}
		if _, err := directoryInfo(bound); err != nil {
			return "", false, err
		}
		return filepath.Clean(bound.Resolved.Abs), true, nil
	}
	if reject := toolrejection.AsToolReject(err); reject != nil && reject.Code == "UNKNOWN_ROOT_LABEL" {
		return "", false, nil
	}
	return "", false, err
}

// printWorkingDirectory implements pwd: logical by default and with -L, the
// physical directory with -P.
func (p *ProgramPaths) printWorkingDirectory(ctx context.Context, physical bool, output io.Writer) error {
	if _, err := p.Bind(ctx, ".", false); err != nil {
		return err
	}
	dir := p.Directory()
	if physical {
		canonical, err := p.Canonical(ctx, dir)
		if err != nil {
			return err
		}
		dir = canonical
	}
	_, err := io.WriteString(output, dir+"\n")
	return err
}

// changeDirectory implements cd. The default logical form resolves `..`
// against the directory text, as POSIX cd -L does, so the result never
// depends on where a link pointed; -P keeps the physical walk. `cd -`
// returns to OLDPWD and prints it; no operand returns to the program's
// starting directory.
func (p *ProgramPaths) changeDirectory(ctx context.Context, operand string, physical bool, output io.Writer) error {
	switch operand {
	case "":
		info, err := directoryInfo(p.initial)
		if err != nil {
			return err
		}
		if !os.SameFile(info, p.initialInfo) {
			return invalidOperand("cd", "initial invocation directory identity changed")
		}
		p.moveTo(p.initial, info)
		return nil
	case "-":
		previous, ok := p.PreviousDirectory()
		if !ok {
			return &UtilityError{Message: "OLDPWD not set"}
		}
		if err := p.Chdir(ctx, previous, physical); err != nil {
			return err
		}
		_, err := io.WriteString(output, p.Directory()+"\n")
		return err
	}
	return p.Chdir(ctx, operand, physical)
}

// Chdir changes only this invocation's cwd after descriptor-relative
// inspection. A logical change cleans the addressed text first.
func (p *ProgramPaths) Chdir(ctx context.Context, operand string, physical bool) error {
	target := operand
	if !physical {
		if !filepath.IsAbs(target) && p.cwd.Resolved.Abs != "" {
			target = p.cwd.Resolved.Abs + string(filepath.Separator) + target
		}
		target = filepath.Clean(target)
	}
	bound, err := p.Bind(ctx, target, false)
	if err != nil {
		return utilityPathError(operand, err)
	}
	bound, info, err := p.directoryBinding(ctx, bound)
	if err != nil {
		return utilityPathError(operand, err)
	}
	if physical {
		bound.exactRelative = readRelative(bound.Resolved.ScopeRel)
		bound.Resolved.Abs = fspath.CanonicalPath(filepath.Join(bound.Resolved.Root.Path, bound.Resolved.ScopeRel))
	}
	p.moveTo(bound, info)
	return nil
}

func (p *ProgramPaths) moveTo(bound BoundPath, info os.FileInfo) {
	p.previous, p.previousInfo, p.hasPrevious = p.cwd, p.cwdInfo, true
	p.cwd, p.cwdInfo = bound, info
}

// UtilityError is an ordinary utility failure: printed on the utility's
// stderr in its own voice with status 1, never a host refusal.
type UtilityError struct {
	Operand string
	Message string
}

func (e *UtilityError) Error() string {
	if e.Operand == "" {
		return e.Message
	}
	return e.Operand + ": " + e.Message
}

// errNotDirectory marks a bound directory operand that names another entry.
var errNotDirectory = errors.New("directory required")
