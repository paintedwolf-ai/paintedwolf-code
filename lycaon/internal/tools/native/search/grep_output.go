package search

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// errBinaryStop ends one binary file's output after its notice.
var errBinaryStop = errors.New("binary file matched")

type nullTrackingReader struct {
	r         io.Reader
	offset    int
	firstNull *int
}

// binaryRecordReader treats NUL as a record boundary while selecting a binary
// file. This bounds the regex input to actual binary records rather than the
// entire LF-free file, without changing byte offsets or the binary notice.
type binaryRecordReader struct{ r io.Reader }

func (b binaryRecordReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	for i := range p[:n] {
		if p[i] == 0 {
			p[i] = '\n'
		}
	}
	return n, err
}

func (n *nullTrackingReader) Read(p []byte) (int, error) {
	read, err := n.r.Read(p)
	if read > 0 && *n.firstNull < 0 {
		if idx := bytes.IndexByte(p[:read], 0); idx >= 0 {
			*n.firstNull = n.offset + idx
		}
	}
	n.offset += read
	return read, err
}

// searchContent selects records of one in-memory input.
func (r *grepRun) searchContent(label string, content []byte, pin *toolcontract.SourcePin, names bool) error {
	return r.searchStream(label, bytes.NewReader(content), 0, len(content), bytes.IndexByte(content, 0), pin, names)
}

// searchNoMatch emits the zero-match file outcome when the index prunes a file.
func (r *grepRun) searchNoMatch(label string, names bool) error {
	switch r.mode {
	case modeCount:
		if r.rg {
			return nil
		}
		if names {
			return r.inv.out.text(label, r.nameEnd(":"), "0\n")
		}
		return r.inv.out.text("0\n")
	case modeFilesWithout:
		return r.inv.out.text(label, r.nameEnd("\n"))
	default:
		return nil
	}
}

// searchStream selects records of one input and prints them in GNU form:
// PATH:LINE:TEXT for selected lines, PATH-LINE-TEXT for context and "--"
// between non-adjacent groups. A file holding NUL bytes is binary: its first
// output line becomes one binary-match notice on the utility's normal stream.
// A selected line with invalid UTF-8 is binary from that
// line on, as GNU grep does in a UTF-8 locale.
func (r *grepRun) searchStream(label string, reader io.Reader, maxBytes int64, totalBytes int, firstNull int, pin *toolcontract.SourcePin, names bool) error {
	inv := r.inv
	if r.json != nil {
		content, err := inv.readAll(reader)
		if err != nil {
			return err
		}
		return r.jsonContent(label, content)
	}
	if r.binaryMode == "without-match" && firstNull >= 0 {
		reader = bytes.NewReader(nil)
		firstNull = -1
	}
	r.numberWidth = len(strconv.Itoa(totalBytes))
	if !r.scan.NullData && r.binaryMode != "text" {
		if r.binaryMode == "without-match" && firstNull >= 0 {
			reader, firstNull = bytes.NewReader(nil), -1
		} else if firstNull < 0 {
			reader = &nullTrackingReader{r: reader, firstNull: &firstNull}
		} else {
			reader = binaryRecordReader{r: reader}
		}
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
	onExceed := func(maximum, observed int64) error {
		return tools.CommandLimitExceeded("stream_throughput_bytes", maximum, observed)
	}
	n, err := textsearch.ScanReader(inv.ctx, reader, maxBytes, onExceed, r.matcher, scan, func(line textsearch.Line) error {
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
		record := line.Text
		binary := !r.scan.NullData && r.binaryMode != "text" && firstNull >= 0
		if binary || (r.binaryMode != "text" && line.Kind == textsearch.SelectedLine && !utf8.Valid(record)) {
			if line.Kind != textsearch.SelectedLine {
				return nil
			}
			offset := firstNull
			if offset < 0 {
				offset = line.Start
			}
			if err := r.binaryNotice(label, names, offset); err != nil {
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
		var budget *textsearch.BudgetError
		if errors.As(err, &budget) {
			inv.warn(false, "%s: search limit %s exceeded (maximum %d, observed %d)", label, budget.Resource, budget.Maximum, budget.Observed)
		}
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

// binaryNotice reports a matching binary file: grep on stderr; rg on stdout
// with the offset of the byte that made the file binary, prefixed by the
// file name when several files are searched.
func (r *grepRun) binaryNotice(label string, names bool, offset int) error {
	if !r.rg {
		r.inv.warn(false, "%s: binary file matches", label)
		return nil
	}
	prefix := ""
	if names {
		prefix = label + ": "
	}
	return r.inv.out.text(fmt.Sprintf("%sbinary file matches (found \"\\0\" byte around offset %d)\n", prefix, offset))
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
			defer replaced.close()
			return true, r.writeRecord(prefix(line.Start), replaced.bytes, nil, 0)
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
			replacement := &grepRecord{inv: r.inv}
			defer replacement.close()
			if err := replacement.replacement(*r.replace, record, groups); err != nil {
				return err
			}
			return r.writeRecord(prefix(line.Start+groups[0]), replacement.bytes, nil, 0)
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

func (r *grepRun) replaceAll(record []byte) (*grepRecord, error) {
	out := &grepRecord{inv: r.inv}
	last := 0
	err := r.matcher.Groups(r.inv.ctx, record, func(groups []int) error {
		if err := out.data(record[last:groups[0]]); err != nil {
			return err
		}
		if err := out.replacement(*r.replace, record, groups); err != nil {
			return err
		}
		last = groups[1]
		return nil
	})
	if err == nil {
		err = out.data(record[last:])
	}
	if err != nil {
		out.close()
		return nil, err
	}
	return out, nil
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
	for _, digit := range body {
		if digit < '0' || digit > '9' {
			return 0, 0, false
		}
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
