package textutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// runUniq collapses adjacent records whose comparison keys are equal: after
// skipping -f fields (blanks then non-blanks) and -s bytes, at most -w bytes,
// ASCII case-insensitive with -i. -c prefixes the count right-aligned in
// seven columns and a space; -d keeps repeated groups, -u single ones, both
// none. -z separates records with NUL. OUTPUT is staged pinned publication.
func runUniq(inv *invocation) error {
	operands := inv.operands()
	if len(operands) > 2 {
		return inv.usage(operands[2], "at most INPUT and OUTPUT")
	}
	key, err := parseUniqKey(inv)
	if err != nil {
		return err
	}
	sources, err := inv.openInputs(operands[:min(len(operands), 1)])
	if err != nil || len(sources) == 0 {
		return err
	}
	defer closeSources(sources)
	records, err := inv.records(sources[0].reader, key.delimiter)
	if err != nil {
		return err
	}
	if len(operands) == 2 {
		return uniqOutput(inv, operands, func(out *nativeutility.Output) error { return uniqRecords(inv, key, records, out) })
	}
	return uniqRecords(inv, key, records, inv.out)
}

type uniqKey struct {
	fields, chars, width int
	fold                 bool
	delimiter            byte
}

func parseUniqKey(inv *invocation) (uniqKey, error) {
	key := uniqKey{fold: inv.flag("-i"), width: -1, delimiter: '\n'}
	if inv.flag("-z") {
		key.delimiter = 0
	}
	for flag, target := range map[string]*int{"-f": &key.fields, "-s": &key.chars, "-w": &key.width} {
		if value, ok := inv.value(flag); ok {
			n, err := strconv.Atoi(value.Value)
			if err != nil || n < 0 || strings.HasPrefix(value.Value, "+") {
				return key, inv.usage(value, "a count from 0")
			}
			*target = n
		}
	}
	return key, nil
}

// text is the part of a record uniq compares.
func (k uniqKey) text(record []byte) []byte {
	i := 0
	for field := 0; field < k.fields; field++ {
		for i < len(record) && (record[i] == ' ' || record[i] == '\t') {
			i++
		}
		for i < len(record) && record[i] != ' ' && record[i] != '\t' {
			i++
		}
	}
	record = record[min(i+k.chars, len(record)):]
	if k.width >= 0 && k.width < len(record) {
		record = record[:k.width]
	}
	return record
}

func (k uniqKey) equal(a, b []byte) bool {
	a, b = k.text(a), k.text(b)
	if !k.fold {
		return bytes.Equal(a, b)
	}
	return compareFolded(a, b, true) == 0
}

// uniqRecords writes the collapsed records to out.
func uniqRecords(inv *invocation, key uniqKey, records *records, out io.Writer) error {
	count, repeated, single := inv.flag("-c"), inv.flag("-d"), inv.flag("-u")
	var previous []byte
	run := 0
	flush := func() error {
		if run == 0 || (repeated && run == 1) || (single && run > 1) {
			return nil
		}
		if count {
			if _, err := fmt.Fprintf(out, "%7d ", run); err != nil {
				return err
			}
		}
		return writeRecord(out, previous, key.delimiter)
	}
	for {
		record, err := records.next()
		if errors.Is(err, io.EOF) {
			return flush()
		}
		if err != nil {
			return err
		}
		if run > 0 && key.equal(previous, record) {
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
