package textutil

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// runUniq collapses adjacent exact-byte records. -c prefixes an unpadded
// count and a space; -d keeps repeated groups, -u single ones, both none.
// An OUTPUT operand is a staged owner publication this frontend refuses.
func runUniq(inv *invocation) error {
	operands := inv.operands()
	if len(operands) > 2 {
		return inv.usage(operands[2], "at most INPUT and OUTPUT")
	}
	sources, err := inv.openInputs(operands[:min(len(operands), 1)])
	if err != nil || len(sources) == 0 {
		return err
	}
	defer closeSources(sources)
	records, err := inv.records(sources[0].reader, '\n')
	if err != nil {
		return err
	}
	if len(operands) == 2 {
		return uniqOutput(inv, operands, func(out *nativeutility.Output) error { return uniqRecords(inv, records, out) })
	}
	return uniqRecords(inv, records, inv.out)
}

// uniqRecords writes the collapsed records to out.
func uniqRecords(inv *invocation, records *records, out io.Writer) error {
	count, repeated, single := inv.flag("-c"), inv.flag("-d"), inv.flag("-u")
	var previous []byte
	run := 0
	flush := func() error {
		if run == 0 || (repeated && run == 1) || (single && run > 1) {
			return nil
		}
		if count {
			if _, err := io.WriteString(out, strconv.Itoa(run)+" "); err != nil {
				return err
			}
		}
		if _, err := out.Write(previous); err != nil {
			return err
		}
		_, err := out.Write([]byte{'\n'})
		return err
	}
	for {
		record, err := records.next()
		if errors.Is(err, io.EOF) {
			return flush()
		}
		if err != nil {
			return err
		}
		if run > 0 && bytes.Equal(record, previous) {
			run++
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		if cap(previous) < len(record) {
			if err := inv.reserve(int64(len(record))); err != nil {
				return err
			}
		}
		previous = append(previous[:0], record...)
		run = 1
	}
}

// selection is a cut LIST: one-based positions as a union of ranges.
type selection [][2]int

func parseSelection(text string) (selection, bool) {
	var out selection
	for _, part := range strings.Split(text, ",") {
		low, high, ranged := strings.Cut(part, "-")
		start, end := 1, int(^uint(0)>>1)
		var err error
		if low != "" {
			if start, err = strconv.Atoi(low); err != nil || start < 1 {
				return nil, false
			}
		}
		if !ranged {
			end = start
		} else if high != "" {
			if end, err = strconv.Atoi(high); err != nil || end < start {
				return nil, false
			}
		}
		if low == "" && (!ranged || high == "") {
			return nil, false
		}
		out = append(out, [2]int{start, end})
	}
	return out, len(out) > 0
}

func (s selection) has(position int) bool {
	for _, r := range s {
		if position >= r[0] && position <= r[1] {
			return true
		}
	}
	return false
}

// runCut selects fields (-f, default TAB, -d one byte), bytes (-b) or Unicode
// scalars (-c) from each record, in record order with no duplicates. A record
// without the delimiter is kept whole. Invalid UTF-8 refuses -c.
func runCut(inv *invocation) error {
	var mode string
	var list commandprogram.Field
	for _, key := range []string{"-f", "-c", "-b"} {
		if value, ok := inv.value(key); ok {
			if mode != "" {
				return inv.usage(value, "exactly one of -f, -c and -b")
			}
			mode, list = key, value
		}
	}
	if mode == "" {
		return inv.usage(inv.commandWord(), "one of -f, -c and -b")
	}
	selected, ok := parseSelection(list.Value)
	if !ok {
		return inv.usage(list, "LIST of N, N-M, -M and N- positions from 1")
	}
	delimiter := byte('\t')
	if sep, ok := inv.value("-d"); ok {
		if mode != "-f" {
			return inv.usage(sep, "-d only with -f")
		}
		if len(sep.Value) != 1 {
			return inv.usage(sep, "a one-byte delimiter")
		}
		delimiter = sep.Value[0]
	}
	sources, err := inv.openInputs(inv.operands())
	if err != nil {
		return err
	}
	defer closeSources(sources)
	for _, source := range sources {
		records, err := inv.records(source.reader, '\n')
		if err != nil {
			return err
		}
		for {
			record, err := records.next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if mode == "-c" && !utf8.Valid(record) {
				return inv.usage(list, "valid UTF-8 input for -c")
			}
			if err := inv.writeRecord(cutRecord(record, mode, selected, delimiter), '\n'); err != nil {
				return err
			}
		}
	}
	return nil
}

func cutRecord(record []byte, mode string, selected selection, delimiter byte) []byte {
	var out []byte
	switch mode {
	case "-b":
		for i := range record {
			if selected.has(i + 1) {
				out = append(out, record[i])
			}
		}
	case "-c":
		position := 0
		for i := 0; i < len(record); {
			_, size := utf8.DecodeRune(record[i:])
			position++
			if selected.has(position) {
				out = append(out, record[i:i+size]...)
			}
			i += size
		}
	default:
		if bytes.IndexByte(record, delimiter) < 0 {
			return record
		}
		first := true
		for i, field := range bytes.Split(record, []byte{delimiter}) {
			if !selected.has(i + 1) {
				continue
			}
			if !first {
				out = append(out, delimiter)
			}
			out = append(out, field...)
			first = false
		}
	}
	return out
}
