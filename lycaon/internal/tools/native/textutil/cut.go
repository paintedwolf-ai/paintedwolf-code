package textutil

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

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

// cutSpec is one cut invocation's selector and delimiters.
type cutSpec struct {
	mode       string
	selected   selection
	complement bool
	delimiter  byte
	output     []byte
	only       bool
	blanks     bool
	record     byte
}

// runCut selects fields (-f, default TAB, -d one byte), bytes (-b) or Unicode
// scalars (-c) from each record, in record order with no duplicates, or with
// --complement every other one. Selected fields join with the output
// delimiter (default the input one); with --output-delimiter, separate byte
// and character ranges are joined by it too. BSD -w splits fields on blank
// runs. A record without the delimiter is kept whole unless -s. Invalid
// UTF-8 refuses -c.
func runCut(inv *invocation) error {
	spec, list, err := parseCut(inv)
	if err != nil {
		return err
	}
	sources, err := inv.openInputs(inv.operands())
	if err != nil {
		return err
	}
	defer closeSources(sources)
	for _, source := range sources {
		records, err := inv.records(source.reader, spec.record)
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
			if spec.mode == "-c" && !utf8.Valid(record) {
				return inv.usage(list, "valid UTF-8 input for -c")
			}
			if spec.mode == "-f" && spec.only && !spec.delimited(record) {
				continue
			}
			if err := writeRecord(inv.out, spec.cut(record), spec.record); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseCut(inv *invocation) (cutSpec, commandprogram.Field, error) {
	spec := cutSpec{complement: inv.flag("--complement"), delimiter: '\t', only: inv.flag("-s"), record: '\n'}
	if inv.flag("-z") {
		spec.record = 0
	}
	var list commandprogram.Field
	for _, key := range []string{"-f", "-c", "-b"} {
		if value, ok := inv.value(key); ok {
			if spec.mode != "" {
				return spec, list, inv.usage(value, "exactly one of -f, -c and -b")
			}
			spec.mode, list = key, value
		}
	}
	if spec.mode == "" {
		return spec, list, inv.usage(inv.commandWord(), "one of -f, -c and -b")
	}
	var ok bool
	if spec.selected, ok = parseSelection(list.Value); !ok {
		return spec, list, inv.usage(list, "LIST of N, N-M, -M and N- positions from 1")
	}
	if spec.blanks = inv.flag("-w"); spec.blanks && (spec.mode != "-f" || inv.flag("-d")) {
		return spec, list, inv.usage(inv.commandWord(), "-w only with -f and without -d")
	}
	if sep, ok := inv.value("-d"); ok {
		if spec.mode != "-f" {
			return spec, list, inv.usage(sep, "-d only with -f")
		}
		if len(sep.Value) != 1 {
			return spec, list, inv.usage(sep, "a one-byte delimiter")
		}
		spec.delimiter = sep.Value[0]
	}
	if spec.only && spec.mode != "-f" {
		return spec, list, inv.usage(inv.commandWord(), "-s only with -f")
	}
	if value, ok := inv.value("--output-delimiter"); ok {
		spec.output = []byte(value.Value)
	} else if spec.mode == "-f" {
		spec.output = []byte{spec.delimiter}
	}
	return spec, list, nil
}

// delimited reports a record with a field delimiter (with BSD -w, a blank
// between fields).
func (c cutSpec) delimited(record []byte) bool {
	if c.blanks {
		return len(bytes.Fields(record)) > 1
	}
	return bytes.IndexByte(record, c.delimiter) >= 0
}

func (c cutSpec) chosen(position int) bool { return c.selected.has(position) != c.complement }

func (c cutSpec) cut(record []byte) []byte {
	var out []byte
	if c.mode == "-f" {
		if !c.delimited(record) {
			return record
		}
		fields := bytes.Split(record, []byte{c.delimiter})
		if c.blanks {
			fields = bytes.Fields(record)
		}
		first := true
		for i, field := range fields {
			if !c.chosen(i + 1) {
				continue
			}
			if !first {
				out = append(out, c.output...)
			}
			out = append(out, field...)
			first = false
		}
		return out
	}
	position, previous := 0, -1
	for i := 0; i < len(record); {
		size := 1
		if c.mode == "-c" {
			_, size = utf8.DecodeRune(record[i:])
		}
		position++
		if c.chosen(position) {
			if previous >= 0 && previous != position-1 {
				out = append(out, c.output...)
			}
			out = append(out, record[i:i+size]...)
			previous = position
		}
		i += size
	}
	return out
}
