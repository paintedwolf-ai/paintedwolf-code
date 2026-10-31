package projectpaths

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// canonicalMode is how much of a path must exist to canonicalize it: every
// component (-e), all but a missing last one (the default, and readlink -f),
// or none (-m). Missing components are appended as written, with .. removing
// the component before it.
type canonicalMode int

const (
	canonicalAllButLast canonicalMode = iota
	canonicalExisting
	canonicalMissing
)

// admits reports whether n missing trailing components, the first of which
// failed with cause, still canonicalize.
func (m canonicalMode) admits(n int, cause error) bool {
	switch {
	case n == 0 || m == canonicalMissing:
		return true
	case m == canonicalAllButLast:
		return n == 1 && errors.Is(cause, fs.ErrNotExist)
	}
	return false
}

// canonicalModeOf reads the last of -e and -m; -f and no option are the default.
func canonicalModeOf(bound commandprogram.BoundArgv) canonicalMode {
	mode := canonicalAllButLast
	for _, item := range bound.Items {
		if item.Option == nil {
			continue
		}
		switch item.Option.Key {
		case "-e":
			mode = canonicalExisting
		case "-m":
			mode = canonicalMissing
		case "-f":
			mode = canonicalAllButLast
		}
	}
	return mode
}

// resolveCanonical canonicalizes operand through the confined reader, links
// before .., as the components are walked: the longest existing prefix of the
// components before the first .. resolves at once, then each later component
// resolves against the canonical directory so far. From the first missing
// component on, the rest follows lexically when mode admits it. With lexical
// (-s) no link is resolved.
func (p *ProgramPaths) resolveCanonical(ctx context.Context, operand string, mode canonicalMode, lexical bool) (string, error) {
	if lexical {
		return p.resolveLexical(ctx, operand, mode)
	}
	head, components := splitComponents(operand)
	lead := len(components)
	for i, part := range components {
		if part == ".." {
			lead = i
			break
		}
	}
	var current, address string
	var cause error
	resolved := 0
	for k := lead; k >= 0; k-- {
		prefix := filepath.Join(append([]string{head}, components[:k]...)...)
		if prefix == "" {
			prefix = "."
		}
		actual, at, err := p.canonicalAddress(ctx, prefix)
		if err == nil {
			current, address, resolved = actual, at, k
			break
		}
		if !missingComponent(err) || k == 0 {
			return "", err
		}
		cause = err
	}
	for resolved < len(components) && cause == nil {
		part := components[resolved]
		if part == ".." {
			current, address, resolved = filepath.Dir(current), filepath.Dir(address), resolved+1
			continue
		}
		actual, at, err := p.canonicalAddress(ctx, filepath.Join(address, part))
		if err != nil {
			if !missingComponent(err) {
				return "", err
			}
			cause = err
			break
		}
		current, address, resolved = actual, at, resolved+1
	}
	if !mode.admits(len(components)-resolved, cause) {
		return "", cause
	}
	return appendLexically(current, components[resolved:]), nil
}

// resolveLexical is realpath -s: the operand joined to the canonical working
// directory and cleaned; only -e requires the result to exist.
func (p *ProgramPaths) resolveLexical(ctx context.Context, operand string, mode canonicalMode) (string, error) {
	path := operand
	if !filepath.IsAbs(path) {
		cwd, err := p.Canonical(ctx, ".")
		if err != nil {
			return "", err
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	if mode == canonicalExisting {
		bound, err := p.Bind(ctx, path, false)
		if err == nil {
			_, err = bound.Stat(true)
		}
		if err != nil {
			return "", PathOperationError(operand, err)
		}
	}
	return path, nil
}

// splitComponents separates a rooted operand's root from its components;
// empty and . components are dropped, .. is kept for link-first resolution.
func splitComponents(operand string) (string, []string) {
	head := ""
	if filepath.IsAbs(operand) {
		head = filepath.VolumeName(operand) + string(filepath.Separator)
	}
	var components []string
	for _, part := range strings.FieldsFunc(operand[len(filepath.VolumeName(operand)):], func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		if part != "." {
			components = append(components, part)
		}
	}
	return head, components
}

func appendLexically(path string, components []string) string {
	for _, part := range components {
		if part == ".." {
			path = filepath.Dir(path)
			continue
		}
		path = filepath.Join(path, part)
	}
	return path
}

// missingComponent reports a failure that a shorter prefix may resolve: an
// absent entry or a non-directory where a directory was needed.
func missingComponent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// utilityPathError turns a missing, non-directory, unreadable or looping
// path into the utility's own diagnostic, printed with status 1; host
// refusals and other errors return unchanged.
func utilityPathError(operand string, err error) error {
	var message string
	switch {
	case errors.Is(err, fs.ErrNotExist):
		message = "No such file or directory"
	case errors.Is(err, syscall.ENOTDIR), errors.Is(err, errNotDirectory):
		message = "Not a directory"
	case errors.Is(err, fs.ErrPermission):
		message = "Permission denied"
	case errors.Is(err, syscall.ELOOP):
		message = "Too many levels of symbolic links"
	default:
		return err
	}
	return &UtilityError{Operand: operand, Message: message}
}
