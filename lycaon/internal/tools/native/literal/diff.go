package literal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// diffOptions are the catalogued diff selections; output is unified.
type diffOptions struct {
	context                      int
	brief, allSpace, spaceChange bool
	blankLines, recursive        bool
	header                       string
	maxCells                     int64
}

func runDiff(inv *invocation) error {
	inv.comparison = true
	opts := diffOptions{context: 3, brief: inv.flag("-q"), allSpace: inv.flag("-w"), spaceChange: inv.flag("-b"), blankLines: inv.flag("-B"), recursive: inv.flag("-r")}
	if value, ok := inv.value("-U"); ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return inv.usage(value, "nonnegative context line count")
		}
		opts.context = n
	}
	opts.maxCells = inv.owners.limit("evaluation_memory_bytes") / 4
	opts.header = diffHeader(inv)
	operands := inv.operands()
	if len(operands) != 2 {
		return inv.usage("diff", "exactly two PATH operands")
	}
	left, right, ok, err := inv.diffSides(operands[0], operands[1])
	if err != nil || !ok {
		return err
	}
	switch {
	case left.dir && right.dir:
		return inv.diffDirectories(left.label, right.label, opts)
	case left.dir:
		left.label = path.Join(left.label, path.Base(right.label))
		left.dir = false
	case right.dir:
		right.label = path.Join(right.label, path.Base(left.label))
		right.dir = false
	}
	return inv.diffFiles(left, right, opts, false)
}

// diffHeader is "diff" with the option words as given, for tree headers.
func diffHeader(inv *invocation) string {
	words := []string{"diff"}
	argv := inv.request.Argv.Values()[1:]
	for i := 0; i < len(argv); i++ {
		word := argv[i]
		if word == "--" {
			break
		}
		if len(word) > 1 && word[0] == '-' {
			words = append(words, word)
			if word == "-U" && i+1 < len(argv) {
				words = append(words, argv[i+1])
				i++
			}
		}
	}
	return strings.Join(words, " ")
}

// diffSide is one compared operand.
type diffSide struct {
	label   string
	dir     bool
	modTime time.Time
}

func (inv *invocation) diffSides(left, right string) (diffSide, diffSide, bool, error) {
	a, okA, err := inv.diffSide(left)
	if err != nil {
		return a, a, false, err
	}
	b, okB, err := inv.diffSide(right)
	if err != nil {
		return a, b, false, err
	}
	return a, b, okA && okB, nil
}

// diffSide classifies an operand by following metadata. An absent path may
// still be an accepted unsaved document, which the content capture decides.
func (inv *invocation) diffSide(operand string) (diffSide, bool, error) {
	side := diffSide{label: operand}
	if operand == "-" {
		side.modTime = time.Now().UTC()
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
	reason, ok := ordinary(err)
	switch {
	case !ok:
		return side, false, err
	case errors.Is(err, fs.ErrNotExist) || toolrejection.AsToolReject(err) != nil:
		// Capture decides: an open unsaved document has no file to stat.
		return side, true, nil //nolint:nilerr // Missing metadata still requires pinned source capture, which admits accepted unsaved documents.
	}
	inv.fail(2, operand, reason)
	return side, false, nil
}

// diffFiles compares two captured files and reports their difference.
func (inv *invocation) diffFiles(left, right diffSide, opts diffOptions, tree bool) error {
	a, err := inv.open(left.label, true)
	if err != nil || a == nil {
		return err
	}
	b, err := inv.open(right.label, true)
	if err != nil || b == nil {
		return err
	}
	if bytes.Equal(a.data, b.data) {
		return nil
	}
	if bytes.IndexByte(a.data, 0) >= 0 || bytes.IndexByte(b.data, 0) >= 0 {
		inv.status = max(inv.status, 1)
		return inv.out.text(fmt.Sprintf("Binary files %s and %s differ\n", left.label, right.label))
	}
	oldLines, newLines := diffLines(a.data), diffLines(b.data)
	ops, err := sourceview.DiffScript(oldLines, newLines, opts.equal(), opts.maxCells)
	if errors.Is(err, sourceview.ErrDiffTooLarge) {
		return limitExceeded("evaluation_memory_bytes", opts.maxCells*4, int64(len(oldLines)+1)*int64(len(newLines)+1)*4)
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

// equal compares transformed lines; reported lines stay original.
func (o diffOptions) equal() func(a, b string) bool {
	if !o.allSpace && !o.spaceChange {
		return nil
	}
	return func(a, b string) bool { return o.normalize(a) == o.normalize(b) }
}

func (o diffOptions) normalize(line string) string {
	body, eol := strings.CutSuffix(line, "\n")
	var b strings.Builder
	space := false
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case ' ', '\t', '\r', '\v', '\f':
			space = true
			continue
		}
		if space && o.spaceChange && !o.allSpace && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteByte(body[i])
	}
	if eol {
		b.WriteByte('\n')
	}
	return b.String()
}

// changed reports the ops that count as differences: with -B, a change run
// made only of blank lines is ignored.
func (o diffOptions) changed(ops []sourceview.DiffOp, oldLines, newLines []string) func(int) bool {
	ignored := map[int]bool{}
	if o.blankLines {
		for start := 0; start < len(ops); {
			if ops[start].Tag == ' ' {
				start++
				continue
			}
			end, blank := start, true
			for ; end < len(ops) && ops[end].Tag != ' '; end++ {
				var line string
				if ops[end].Tag == '+' {
					line = newLines[ops[end].New]
				} else {
					line = oldLines[ops[end].Old]
				}
				blank = blank && strings.TrimSpace(line) == ""
			}
			if blank {
				for i := start; i < end; i++ {
					ignored[i] = true
				}
			}
			start = end
		}
	}
	return func(i int) bool { return ops[i].Tag != ' ' && !ignored[i] }
}
