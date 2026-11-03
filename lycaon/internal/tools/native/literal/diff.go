package literal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// diffOptions are the catalogued diff selections; output is unified.
type diffOptions struct {
	context                      int
	brief, allSpace, spaceChange bool
	blankLines, recursive        bool
	// newFile compares an absent file as empty (-N).
	newFile bool
	// stripCR drops a carriage return before each newline on input.
	stripCR bool
	// excludes skips directory entries whose base names match (-x, -X).
	excludes []*regexp.Regexp
	header   string
	maxCells int64
}

func runDiff(inv *invocation) error {
	inv.comparison = true
	opts := diffOptions{context: 3, brief: inv.flag("-q"), allSpace: inv.flag("-w"), spaceChange: inv.flag("-b"), blankLines: inv.flag("-B"),
		recursive: inv.flag("-r"), newFile: inv.flag("-N"), stripCR: inv.flag("--strip-trailing-cr")}
	if value, ok := inv.value("-U"); ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return inv.usage(value, "nonnegative context line count")
		}
		opts.context = n
	}
	var err error
	if opts.excludes, err = inv.diffExcludes(); err != nil {
		return err
	}
	opts.maxCells = inv.owners.limit("evaluation_memory_bytes") / 4
	opts.header = diffHeader(inv)
	operands := inv.argv.Operands()
	if len(operands) != 2 {
		return inv.usage("diff", "exactly two PATH operands")
	}
	left, right, ok, err := inv.diffSides(operands[0], operands[1], opts.newFile)
	if err != nil || !ok {
		return err
	}
	switch {
	case left.dir && right.dir, left.dir && right.absent, left.absent && right.dir:
		return inv.diffDirectories(left, right, opts)
	case left.dir:
		left.label = path.Join(left.label, path.Base(right.label))
		left.dir = false
	case right.dir:
		right.label = path.Join(right.label, path.Base(left.label))
		right.dir = false
	}
	return inv.diffFiles(left, right, opts, false)
}

// diffSide is one compared operand. An absent side exists only under -N and
// compares as an empty file or directory.
type diffSide struct {
	field   commandprogram.Field
	label   string
	dir     bool
	absent  bool
	modTime time.Time
}

func (s diffSide) operandField() commandprogram.Field {
	if s.field.Value == s.label {
		return s.field
	}
	return commandprogram.Field{Value: s.label}
}

// absentSide is the missing counterpart of a path under -N, stamped at the
// epoch as the unified header shows it.
func absentSide(label string) diffSide {
	return diffSide{label: label, absent: true, modTime: time.Unix(0, 0)}
}

func (inv *invocation) diffSides(left, right commandprogram.Field, newFile bool) (diffSide, diffSide, bool, error) {
	a, okA, err := inv.diffSide(left, newFile)
	if err != nil {
		return a, a, false, err
	}
	b, okB, err := inv.diffSide(right, newFile)
	if err != nil {
		return a, b, false, err
	}
	if a.absent && b.absent {
		inv.fail(2, a.label, "No such file or directory")
		inv.fail(2, b.label, "No such file or directory")
		return a, b, false, nil
	}
	return a, b, okA && okB, nil
}

// diffSide classifies an operand by following metadata. An absent path may
// still be an accepted unsaved document, which the content capture decides,
// unless -N makes it an empty side.
func (inv *invocation) diffSide(field commandprogram.Field, newFile bool) (diffSide, bool, error) {
	operand := field.Value
	side := diffSide{field: field, label: operand}
	if operand == "-" {
		side.modTime = time.Now().UTC()
		return side, true, nil
	}
	if field.ProcessSubstitution().IsInput() {
		return side, true, nil
	}
	bound, err := inv.paths.Bind(inv.ctx, operand, false)
	if err == nil {
		st, statErr := bound.Stat(true)
		if statErr == nil {
			side.dir, side.modTime = typeLetter(st.Mode) == 'd', st.ModTime
			return side, true, nil
		}
		err = statErr
	}
	err = projectpaths.PathOperationError(operand, err)
	if newFile && errors.Is(err, fs.ErrNotExist) {
		absent := absentSide(operand)
		absent.field = field
		return absent, true, nil
	}
	reason, ok := ordinary(err)
	if !ok {
		return side, false, err
	}
	// Capture decides whether an absent published path is an unsaved document.
	capture := errors.Is(err, fs.ErrNotExist) || toolrejection.AsToolReject(err) != nil
	if !capture {
		inv.fail(2, operand, reason)
	}
	return side, capture, nil
}

// diffInput is one side's bytes; an absent side is empty.
func (inv *invocation) diffInput(side diffSide, stripCR bool) ([]byte, bool, error) {
	if side.absent {
		return nil, true, nil
	}
	in, err := inv.openField(side.operandField(), true)
	if err != nil || in == nil {
		return nil, false, err
	}
	if stripCR {
		return bytes.ReplaceAll(in.data, []byte("\r\n"), []byte("\n")), true, nil
	}
	return in.data, true, nil
}

// diffFiles compares two captured files and reports their difference.
func (inv *invocation) diffFiles(left, right diffSide, opts diffOptions, tree bool) error {
	a, ok, err := inv.diffInput(left, opts.stripCR)
	if err != nil || !ok {
		return err
	}
	b, ok, err := inv.diffInput(right, opts.stripCR)
	if err != nil || !ok {
		return err
	}
	if bytes.Equal(a, b) {
		return nil
	}
	if bytes.IndexByte(a, 0) >= 0 || bytes.IndexByte(b, 0) >= 0 {
		inv.status = max(inv.status, 1)
		return inv.out.text(fmt.Sprintf("Binary files %s and %s differ\n", left.label, right.label))
	}
	if opts.brief && !opts.allSpace && !opts.spaceChange && !opts.blankLines {
		inv.status = max(inv.status, 1)
		return inv.out.text(fmt.Sprintf("Files %s and %s differ\n", left.label, right.label))
	}
	oldLines, newLines := diffLines(a), diffLines(b)
	ops, err := sourceview.DiffScript(oldLines, newLines, opts.equal(), opts.maxCells)
	if errors.Is(err, sourceview.ErrDiffTooLarge) {
		var capacity *sourceview.DiffLimitError
		if errors.As(err, &capacity) {
			return tools.CommandLimitExceeded("evaluation_memory_bytes", capacity.MaximumCells*4, capacity.ObservedCells*4)
		}
		return err
	}
	if err != nil {
		return err
	}
	changed := opts.changed(ops, oldLines, newLines)
	hunks := sourceview.DiffHunks(ops, opts.context, changed)
	if len(hunks) == 0 {
		return nil
	}
	inv.status = max(inv.status, 1)
	if opts.brief {
		return inv.out.text(fmt.Sprintf("Files %s and %s differ\n", left.label, right.label))
	}
	var out strings.Builder
	if tree {
		out.WriteString(opts.header + " " + left.label + " " + right.label + "\n")
	}
	out.WriteString("--- " + left.label + diffStamp(left.modTime) + "\n")
	out.WriteString("+++ " + right.label + diffStamp(right.modTime) + "\n")
	for _, h := range hunks {
		out.WriteString("@@ -" + diffRange(h.OldStart, h.OldCount) + " +" + diffRange(h.NewStart, h.NewCount) + " @@\n")
		for _, op := range ops[h.StartOp:h.EndOp] {
			var line string
			if op.Tag == '+' {
				line = newLines[op.New]
			} else {
				line = oldLines[op.Old]
			}
			out.WriteByte(op.Tag)
			out.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	return inv.out.text(out.String())
}

// diffLines keeps each line's LF so a missing final newline is a difference.
func diffLines(data []byte) []string {
	var lines []string
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n') + 1
		if end == 0 {
			end = len(data)
		}
		lines = append(lines, string(data[:end]))
		data = data[end:]
	}
	return lines
}

func diffStamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return "\t" + t.UTC().Format("2006-01-02 15:04:05.000000000 -0700")
}

// diffRange is GNU's unified range: "start" for one line, "start,count"
// otherwise, and the preceding line number for an empty range.
func diffRange(start, count int) string {
	switch count {
	case 0:
		return strconv.Itoa(start-1) + ",0"
	case 1:
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(count)
}
