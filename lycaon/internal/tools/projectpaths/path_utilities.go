package projectpaths

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

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
