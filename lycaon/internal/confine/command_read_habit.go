package confine

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/fspath"
)

// ReadHabit is a native read equivalent for one file command.
type ReadHabit struct {
	Program string
	Path    string
	Offset  int // 1-based first line; 0 reads from the top
	Limit   int // maximum lines; 0 reads to EOF
}

// leadingLinesDefault is the leading-lines grammar's count when no -n is given.
const leadingLinesDefault = 10

// lineSpanScript accepts numeric print ranges such as `1,200p`.
var lineSpanScript = regexp.MustCompile(`^(\d+)(?:,(\d+))?p$`)

// pathGlobChars make a path argument stand for a set the read tool cannot expand.
const pathGlobChars = "*?["

// readHabitGrammar parses catalog-selected command argument shapes.
type readHabitGrammar string

const (
	// readHabitWholeFile is `<prog> <file>` — the whole file, no options.
	readHabitWholeFile readHabitGrammar = "whole_file"
	// readHabitLeadingLines is `<prog> [-n N|-N] <file>`.
	readHabitLeadingLines readHabitGrammar = "leading_lines"
	// readHabitQuietLineSpan is `<prog> -n '<start>[,<end>]p' <file>`.
	readHabitQuietLineSpan readHabitGrammar = "quiet_line_span"
)

// ReadHabitFor returns an exact native read equivalent when available.
func ReadHabitFor(command, projectDir string) (ReadHabit, bool) {
	_, program, args, err := argv.SplitCommandLine(strings.TrimSpace(command))
	if err != nil {
		return ReadHabit{}, false
	}
	base := filepath.Base(strings.TrimSpace(program))
	var (
		habit ReadHabit
		ok    bool
	)
	switch readHabitGrammarFor[base] {
	case readHabitWholeFile:
		habit, ok = wholeFileReadHabit(args)
	case readHabitLeadingLines:
		habit, ok = leadingLinesReadHabit(args)
	case readHabitQuietLineSpan:
		habit, ok = quietLineSpanReadHabit(args)
	}
	if !ok {
		return ReadHabit{}, false
	}
	rel, ok := nativeRegularFilePath(habit.Path, projectDir)
	if !ok {
		return ReadHabit{}, false
	}
	habit.Program = base
	habit.Path = rel
	return habit, true
}

// wholeFileReadHabit accepts a bare single-file read.
func wholeFileReadHabit(args []string) (ReadHabit, bool) {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		return ReadHabit{}, false
	}
	return ReadHabit{Path: args[0]}, true
}

// leadingLinesReadHabit maps `[-n N|-N] <file>` to a read of the first N lines.
func leadingLinesReadHabit(args []string) (ReadHabit, bool) {
	limit := leadingLinesDefault
	path := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-n" || arg == "--lines":
			i++
			if i >= len(args) {
				return ReadHabit{}, false
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				return ReadHabit{}, false
			}
			limit = n
		case strings.HasPrefix(arg, "--lines="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--lines="))
			if err != nil || n <= 0 {
				return ReadHabit{}, false
			}
			limit = n
		case strings.HasPrefix(arg, "-") && len(arg) > 1:
			// -20 is a line count; every other flag (-c, --bytes, -q) is not a read.
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "-"))
			if err != nil || n <= 0 {
				return ReadHabit{}, false
			}
			limit = n
		default:
			if path != "" {
				return ReadHabit{}, false
			}
			path = arg
		}
	}
	if path == "" {
		return ReadHabit{}, false
	}
	return ReadHabit{Path: path, Offset: 1, Limit: limit}, true
}

// quietLineSpanReadHabit maps `-n '<start>,<end>p' <file>` to a read of that span.
func quietLineSpanReadHabit(args []string) (ReadHabit, bool) {
	quiet := false
	script := ""
	path := ""
	for _, arg := range args {
		switch {
		case arg == "-n" || arg == "--quiet" || arg == "--silent":
			quiet = true
		case strings.HasPrefix(arg, "-"):
			// -i, -e, -E, -f turn the program into an editor or a multi-script run.
			return ReadHabit{}, false
		case script == "" && lineSpanScript.MatchString(arg):
			script = arg
		case path == "":
			path = arg
		default:
			return ReadHabit{}, false
		}
	}
	if !quiet || script == "" || path == "" {
		return ReadHabit{}, false
	}
	m := lineSpanScript.FindStringSubmatch(script)
	start, err := strconv.Atoi(m[1])
	if err != nil || start <= 0 {
		return ReadHabit{}, false
	}
	if m[2] == "" {
		return ReadHabit{Path: path, Offset: start, Limit: 1}, true
	}
	end, err := strconv.Atoi(m[2])
	if err != nil || end < start {
		return ReadHabit{}, false
	}
	return ReadHabit{Path: path, Offset: start, Limit: end - start + 1}, true
}

// ProjectPathExists checks whether a relative operand names an existing project entry.
func ProjectPathExists(arg, projectDir string) bool {
	arg = strings.TrimSpace(arg)
	if arg == "" || strings.ContainsAny(arg, pathGlobChars) {
		return false
	}
	if strings.HasPrefix(arg, "/") || isWindowsAbsPath(arg) || arg == "~" || strings.HasPrefix(arg, "~/") {
		return false
	}
	abs, ok := resolveArgPath(arg, projectDir)
	if !ok {
		return false
	}
	project := fspath.CanonicalPath(projectDir)
	if project == "" || !strings.HasPrefix(abs, project+"/") {
		return false
	}
	_, err := os.Lstat(abs)
	return err == nil
}

func nativeRegularFilePath(arg, projectDir string) (string, bool) {
	if arg != strings.TrimSpace(arg) {
		return "", false
	}
	if arg == "" || strings.ContainsAny(arg, pathGlobChars) {
		return "", false
	}
	if arg == "~" || strings.HasPrefix(arg, "~/") {
		return "", false
	}
	abs, ok := resolveArgPath(arg, projectDir)
	if !ok {
		return "", false
	}
	if filepath.IsAbs(arg) {
		info, err := os.Lstat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return "", false
		}
		return filepath.ToSlash(abs), true
	}
	project := fspath.CanonicalPath(projectDir)
	if project == "" || !strings.HasPrefix(abs, project+"/") {
		return "", false
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return filepath.ToSlash(strings.TrimPrefix(abs, project+"/")), true
}
