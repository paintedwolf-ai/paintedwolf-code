package projectpaths

import (
	"context"
	"errors"
	"io"
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
// (-s) no link is resolved and only existence is checked.
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

// resolveLexical is realpath -s: the operand joined to the working directory
// and cleaned, checked for the components mode requires.
func (p *ProgramPaths) resolveLexical(ctx context.Context, operand string, mode canonicalMode) (string, error) {
	path := operand
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.Directory(), path)
	}
	path = filepath.Clean(path)
	check := ""
	switch mode {
	case canonicalExisting:
		check = path
	case canonicalAllButLast:
		check = filepath.Dir(path)
	}
	if check != "" {
		bound, err := p.Bind(ctx, check, false)
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
	message := ""
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

// relativeOutput is realpath's --relative-to and --relative-base projection
// of canonical paths.
type relativeOutput struct {
	to, base string
}

// relativeTo canonicalizes the --relative-to and --relative-base directories.
// Without --relative-to, paths are relative to the base; when the
// --relative-to directory is outside the base, every path stays absolute.
func (p *ProgramPaths) relativeTo(ctx context.Context, bound commandprogram.BoundArgv, mode canonicalMode, lexical bool) (relativeOutput, error) {
	var out relativeOutput
	for _, key := range []string{"--relative-to", "--relative-base"} {
		option, ok := bound.Last(key)
		if !ok {
			continue
		}
		dir, err := p.resolveCanonical(ctx, option.Values[0].Value, mode, lexical)
		if err != nil {
			return out, err
		}
		if key == "--relative-to" {
			out.to = dir
		} else {
			out.base = dir
		}
	}
	if out.to == "" {
		out.to = out.base
	}
	if out.base != "" && !underPath(out.base, out.to) {
		out.to, out.base = "", ""
	}
	return out, nil
}

func (r relativeOutput) project(path string) string {
	if r.to == "" || (r.base != "" && !underPath(r.base, path)) {
		return path
	}
	from := strings.Split(strings.Trim(r.to, string(filepath.Separator)), string(filepath.Separator))
	target := strings.Split(strings.Trim(path, string(filepath.Separator)), string(filepath.Separator))
	if from[0] == "" {
		from = nil
	}
	if target[0] == "" {
		target = nil
	}
	common := 0
	for common < len(from) && common < len(target) && from[common] == target[common] {
		common++
	}
	parts := make([]string, 0, len(from)-common+len(target)-common)
	for range len(from) - common {
		parts = append(parts, "..")
	}
	parts = append(parts, target[common:]...)
	if len(parts) == 0 {
		return "."
	}
	return strings.Join(parts, string(filepath.Separator))
}

func underPath(base, path string) bool {
	if base == string(filepath.Separator) {
		return strings.HasPrefix(path, base)
	}
	return path == base || strings.HasPrefix(path, base+string(filepath.Separator))
}

// realpathOutput prints each operand's canonical path. An operand that does
// not resolve is reported on stderr (unless -q) and the rest continue.
func (p *ProgramPaths) realpathOutput(ctx context.Context, bound commandprogram.BoundArgv, args []string, output, errOutput io.Writer) (int, error) {
	mode, lexical := canonicalModeOf(bound), hasOption(bound, "-s")
	relative, err := p.relativeTo(ctx, bound, mode, lexical)
	if err != nil {
		return 0, utilityPathError(firstOption(bound, "--relative-to", "--relative-base"), err)
	}
	terminator := "\n"
	if hasOption(bound, "-z") {
		terminator = "\x00"
	}
	status := 0
	for _, arg := range args {
		path, err := p.resolveCanonical(ctx, arg, mode, lexical)
		if err != nil {
			var utility *UtilityError
			if !errors.As(utilityPathError(arg, err), &utility) {
				return status, err
			}
			if !hasOption(bound, "-q") {
				if _, err := io.WriteString(errOutput, "realpath: "+utility.Error()+"\n"); err != nil {
					return status, err
				}
			}
			status = 1
			continue
		}
		path = relative.project(path)
		if err := validateOutputPath(path, terminator == "\x00"); err != nil {
			return status, err
		}
		if _, err := io.WriteString(output, path+terminator); err != nil {
			return status, err
		}
	}
	return status, nil
}

// readlinkOutput prints link targets, or canonical paths under -f, -e or -m.
// Like readlink, a failure prints nothing and sets status 1.
func (p *ProgramPaths) readlinkOutput(ctx context.Context, bound commandprogram.BoundArgv, args []string, output io.Writer) (int, error) {
	canonical := hasOption(bound, "-f") || hasOption(bound, "-e") || hasOption(bound, "-m")
	status := 0
	for _, arg := range args {
		var path string
		var err error
		if canonical {
			path, err = p.resolveCanonical(ctx, arg, canonicalModeOf(bound), false)
		} else {
			path, err = p.LinkTarget(ctx, arg)
		}
		if err != nil {
			var utility *UtilityError
			if !errors.As(utilityPathError(arg, err), &utility) && !errors.Is(err, syscall.EINVAL) {
				return status, err
			}
			status = 1
			continue
		}
		if err := validateOutputPath(path, false); err != nil {
			return status, err
		}
		if _, err := io.WriteString(output, path+"\n"); err != nil {
			return status, err
		}
	}
	return status, nil
}

// basenameOutput is basename NAME [SUFFIX], or with -a or -s every operand
// as a name; -z ends each with NUL.
func basenameOutput(bound commandprogram.BoundArgv, args []string, output io.Writer, usage func(string) error) error {
	suffix, multiple := "", hasOption(bound, "-a")
	if option, ok := bound.Last("-s"); ok {
		suffix, multiple = option.Values[0].Value, true
	}
	switch {
	case len(args) == 0:
		return usage("missing operand")
	case !multiple && len(args) > 2:
		return usage("extra operand '" + args[2] + "'")
	case !multiple:
		if len(args) == 2 {
			suffix = args[1]
		}
		args = args[:1]
	}
	terminator := "\n"
	if hasOption(bound, "-z") {
		terminator = "\x00"
	}
	for _, arg := range args {
		if _, err := io.WriteString(output, PathBase(arg, suffix)+terminator); err != nil {
			return err
		}
	}
	return nil
}

func hasOption(bound commandprogram.BoundArgv, key string) bool {
	_, ok := bound.Last(key)
	return ok
}

func firstOption(bound commandprogram.BoundArgv, keys ...string) string {
	for _, key := range keys {
		if option, ok := bound.Last(key); ok {
			return option.Values[0].Value
		}
	}
	return ""
}
