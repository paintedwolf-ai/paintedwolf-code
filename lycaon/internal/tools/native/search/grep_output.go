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

// searchStream selects records of one input and prints them in GNU form:
// PATH:LINE:TEXT for selected lines, PATH-LINE-TEXT for context and "--"
// between non-adjacent groups. A file holding NUL bytes is binary: its first
// output line becomes one "grep: PATH: binary file matches" notice on stderr
// (GNU 3.5 and later). A selected line with invalid UTF-8 is binary from that
// line on, as GNU grep does in a UTF-8 locale.
func (r *grepRun) searchStream(label string, reader io.Reader, maxBytes int64, totalBytes int, firstNull int, pin *toolcontract.SourcePin, names bool) error {
	if r.json != nil {
		var data []byte
		var err error
		if maxBytes > 0 {
			data, err = io.ReadAll(io.LimitReader(reader, maxBytes+1))
			if err == nil && int64(len(data)) > maxBytes {
				err = tools.CommandLimitExceeded("stream_throughput_bytes", maxBytes, int64(len(data)))
			}
		} else {
			data, err = io.ReadAll(reader)
		}
		if err != nil {
			return err
		}
		return r.jsonContent(label, data)
	}
	inv := r.inv
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
		record := line.Text
		binary := !r.scan.NullData && r.binaryMode != "text" && firstNull >= 0
		if r.binaryMode == "without-match" && (binary || (line.Kind == textsearch.SelectedLine && !utf8.Valid(record))) {
			return errBinaryStop
		}
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
		if binary || (r.binaryMode != "text" && line.Kind == textsearch.SelectedLine && !utf8.Valid(record)) {
			if line.Kind != textsearch.SelectedLine {
				return nil
			}
			if err := r.binaryNotice(label); err != nil {
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

func (r *grepRun) binaryNotice(label string) error {
	if !r.rg {
		r.inv.warn(false, "Binary file %s matches", label)
		return nil
	}
	return r.inv.out.text(fmt.Sprintf("Binary file %s matches\n", label))
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
