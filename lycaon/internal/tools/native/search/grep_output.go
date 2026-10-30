package search

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// errBinaryStop ends one binary file's output after its notice.
var errBinaryStop = errors.New("binary file matched")

// searchContent selects records of one input and prints them in GNU form:
// PATH:LINE:TEXT for selected lines, PATH-LINE-TEXT for context and "--"
// between non-adjacent groups. A file holding NUL bytes is binary: its first
// output line becomes one "grep: PATH: binary file matches" notice on stderr
// (GNU 3.5 and later). A selected line with invalid UTF-8 is binary from that
// line on, as GNU grep does in a UTF-8 locale.
func (r *grepRun) searchContent(label string, content []byte, pin *toolcontract.SourcePin, names bool) error {
	if r.json != nil {
		return r.jsonContent(label, content)
	}
	inv := r.inv
	r.numberWidth = len(strconv.Itoa(len(content)))
	binary := !r.scan.NullData && r.binaryMode != "text" && bytes.IndexByte(content, 0) >= 0
	if binary && r.binaryMode == "without-match" {
		content, binary = nil, false
	}
	listing := r.mode != modeLines
	separator := false
	emitted := false
	selectedHere := 0
	scan := r.scan
	if r.mode == modeFilesWith || r.mode == modeFilesWithout || r.mode == modeQuiet {
		scan.MaxSelected, scan.Before, scan.After = 1, 0, 0
	}
	if listing {
		scan.Before, scan.After = 0, 0
	}
	n, err := textsearch.ScanLines(inv.ctx, content, r.matcher, scan, func(line textsearch.Line) error {
		if line.Kind == textsearch.SelectedLine {
			selectedHere++
			r.selected = true
			if r.mode == modeQuiet {
				return errQuietMatch
			}
		}
		if listing {
			return nil
		}
		record := content[line.Start:line.End]
		if binary || (r.binaryMode != "text" && line.Kind == textsearch.SelectedLine && !utf8.Valid(record)) {
			if line.Kind != textsearch.SelectedLine {
				return nil
			}
			if err := r.binaryNotice(label, names, line, content); err != nil {
				return err
			}
			return errBinaryStop
		}
		separator = separator || line.Gap
		if r.only && line.Kind == textsearch.ContextLine {
			return nil
		}
		if separator && emitted {
			if err := inv.out.text("--\n"); err != nil {
				return err
			}
		}
		separator = false
		written, err := r.printLine(label, names, line, record, pin)
		emitted = emitted || written
		return err
	})
	if errors.Is(err, errBinaryStop) {
		err = nil
	}
	if err != nil {
		return err
	}
	switch r.mode {
	case modeLines, modeQuiet, modeFileList:
		return nil
	case modeCount:
		if r.rg && n == 0 {
			return nil
		}
		if names {
			return inv.out.text(label, r.nameEnd(":"), strconv.Itoa(n), "\n")
		}
		return inv.out.text(strconv.Itoa(n), "\n")
	case modeFilesWith:
		if n > 0 {
			return inv.out.text(label, r.nameEnd("\n"))
		}
	case modeFilesWithout:
		if n == 0 {
			return inv.out.text(label, r.nameEnd("\n"))
		}
	}
	return nil
}

func (r *grepRun) binaryNotice(label string, names bool, line textsearch.Line, content []byte) error {
	if !r.rg {
		r.inv.warn(false, "%s: binary file matches", label)
		return nil
	}
	offset := bytes.IndexByte(content, 0)
	if offset < 0 {
		offset = line.Start
	}
	notice := fmt.Sprintf("binary file matches (found \"\\0\" byte around offset %d)\n", offset)
	if names {
		return r.inv.out.text(label, ": ", notice)
	}
	return r.inv.out.text(notice)
}

// printLine writes one selected or context record; with -o it writes each
// match of a selected record on its own line.
func (r *grepRun) printLine(label string, names bool, line textsearch.Line, record []byte, pin *toolcontract.SourcePin) (bool, error) {
	sep := ":"
	if line.Kind == textsearch.ContextLine {
		sep = "-"
	}
	prefix := func(offset int) string {
		var b strings.Builder
		if names {
			b.WriteString(label + r.nameEnd(sep))
		}
		if r.numbers {
			b.WriteString(r.number(line.Number) + sep)
		}
		if r.offsets {
			b.WriteString(r.number(offset) + sep)
		}
		if r.tabs && b.Len() > 0 {
			b.WriteByte('\t')
		}
		return b.String()
	}
	if !r.only {
		if r.replace != nil && line.Kind == textsearch.SelectedLine && !r.scan.Invert {
			replaced, err := r.replaceAll(record)
			if err != nil {
				return false, err
			}
			return true, r.writeRecord(prefix(line.Start), replaced, nil, 0)
		}
		return true, r.writeRecord(prefix(line.Start), record, pin, line.Start)
	}
	if r.scan.Invert {
		return false, nil
	}
	wrote := false
	err := r.matcher.Groups(r.inv.ctx, record, func(groups []int) error {
		wrote = true
		if r.replace != nil {
			return r.writeRecord(prefix(line.Start+groups[0]), expandReplacement(*r.replace, record, groups), nil, 0)
		}
		return r.writeRecord(prefix(line.Start+groups[0]), record[groups[0]:groups[1]], pin, line.Start+groups[0])
	})
	return wrote, err
}

// writeRecord prints one output record; text copied from a pinned capture
// at offset is mapped back to it.
func (r *grepRun) writeRecord(prefix string, text []byte, pin *toolcontract.SourcePin, offset int) error {
	if err := r.inv.out.text(prefix); err != nil {
		return err
	}
	if err := r.inv.out.literal(pin, text, int64(offset)); err != nil {
		return err
	}
	if r.scan.NullData {
		return r.inv.out.text("\x00")
	}
	return r.inv.out.text("\n")
}

// nameEnd is what follows a file name: NUL under -Z, otherwise sep.
func (r *grepRun) nameEnd(sep string) string {
	if r.nullNames {
		return "\x00"
	}
	return sep
}

// number renders a line number or byte offset; under -T GNU grep
// right-aligns it to the digit count of the input's size.
func (r *grepRun) number(n int) string {
	if r.tabs {
		return fmt.Sprintf("%*d", r.numberWidth, n)
	}
	return strconv.Itoa(n)
}

func (r *grepRun) replaceAll(record []byte) ([]byte, error) {
	var out []byte
	last := 0
	err := r.matcher.Groups(r.inv.ctx, record, func(groups []int) error {
		out = append(out, record[last:groups[0]]...)
		out = append(out, expandReplacement(*r.replace, record, groups)...)
		last = groups[1]
		return nil
	})
	return append(out, record[last:]...), err
}

// checkReplacement admits $N, ${N} and $$ for the groups the pattern has.
func checkReplacement(replacement string, groups int) error {
	for i := 0; i < len(replacement); i++ {
		if replacement[i] != '$' || i+1 >= len(replacement) {
			continue
		}
		n, size, ok := replacementGroup(replacement[i+1:])
		if !ok {
			continue
		}
		if n > groups {
			return fmt.Errorf("capture group $%d not defined by the pattern (it defines %d)", n, groups)
		}
		i += size
	}
	return nil
}

func replacementGroup(s string) (int, int, bool) {
	if strings.HasPrefix(s, "$") {
		return -1, 1, false
	}
	braced := strings.HasPrefix(s, "{")
	var body string
	if braced {
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return 0, 0, false
		}
		body = s[1:end]
	} else {
		end := 0
		for end < len(s) && s[end] >= '0' && s[end] <= '9' {
			end++
		}
		body = s[:end]
	}
	n, err := strconv.Atoi(body)
	if body == "" || err != nil {
		return 0, 0, false
	}
	if braced {
		return n, len(body) + 2, true
	}
	return n, len(body), true
}

func expandReplacement(replacement string, record []byte, groups []int) []byte {
	var out []byte
	for i := 0; i < len(replacement); i++ {
		c := replacement[i]
		if c != '$' || i+1 >= len(replacement) {
			out = append(out, c)
			continue
		}
		if replacement[i+1] == '$' {
			out = append(out, '$')
			i++
			continue
		}
		n, size, ok := replacementGroup(replacement[i+1:])
		if !ok {
			out = append(out, c)
			continue
		}
		if 2*n+1 < len(groups) && groups[2*n] >= 0 {
			out = append(out, record[groups[2*n]:groups[2*n+1]]...)
		}
		i += size
	}
	return out
}

// rgHelp is rg -h: the catalogued form list.
func rgHelp(spec toolcontract.CommandSpecification) string {
	var b strings.Builder
	b.WriteString("Usage: " + spec.Argv + "\n\nOptions:\n")
	for _, form := range spec.Forms {
		b.WriteString("  " + strings.Join(form.Spellings, ", "))
		if form.Arity > 0 {
			b.WriteString(" " + strings.ToUpper(strings.ReplaceAll(form.ValueRole, "-", "_")))
		}
		b.WriteString("\n")
	}
	return b.String()
}
