package textutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// sortKind is how one key compares.
type sortKind uint8

const (
	sortBytes sortKind = iota
	sortNumeric
	sortHuman
	sortVersion
)

// sortKey is one -k START[,END]: one-based fields and characters (bytes).
// endField 0 runs to the end of the record; endChar 0 to the end of its field.
// blankStart and blankEnd skip leading blanks before counting characters.
type sortKey struct {
	startField, startChar int
	endField, endChar     int
	kind                  sortKind
	fold, reverse         bool
	blankStart, blankEnd  bool
}

// sortOrder is the complete comparison: keys in order, then the whole record
// in byte order unless -s or -u, where records equal on every key are equal.
// Ordering is unsigned byte order; there is no ambient locale.
type sortOrder struct {
	keys      []sortKey
	separator int
	reverse   bool
	unique    bool
	stable    bool
}

func parseSortOrder(inv *invocation) (sortOrder, error) {
	order := sortOrder{separator: -1, reverse: inv.flag("-r"), unique: inv.flag("-u"), stable: inv.flag("-s")}
	global := sortKey{fold: inv.flag("-f"), reverse: order.reverse, blankStart: inv.flag("-b"), blankEnd: inv.flag("-b")}
	kinds := 0
	for flag, kind := range map[string]sortKind{"-n": sortNumeric, "-h": sortHuman, "-V": sortVersion} {
		if inv.flag(flag) {
			global.kind = kind
			kinds++
		}
	}
	if kinds > 1 {
		return order, inv.usage(inv.commandWord(), "at most one of -n, -h and -V")
	}
	if sep, ok := inv.value("-t"); ok {
		if len(sep.Value) != 1 {
			return order, inv.usage(sep, "a one-byte field separator")
		}
		order.separator = int(sep.Value[0])
	}
	for _, option := range inv.argv.Options("-k") {
		key, modified, err := parseSortKey(option.Values[0].Value)
		if err != nil {
			return order, inv.usage(option.Values[0], "KEY as F[.C][bfhnrV][,F[.C][bfhnrV]]")
		}
		if !modified {
			key.kind, key.fold, key.reverse, key.blankStart, key.blankEnd = global.kind, global.fold, global.reverse, global.blankStart, global.blankEnd
		}
		order.keys = append(order.keys, key)
	}
	if len(order.keys) == 0 {
		global.startField, global.startChar = 1, 1
		order.keys = []sortKey{global}
	}
	return order, nil
}

// parseSortKey reads one key; modified reports any ordering letter, which
// stops the key inheriting the global options.
func parseSortKey(text string) (sortKey, bool, error) {
	start, end, ranged := strings.Cut(text, ",")
	var key sortKey
	modified := false
	var err error
	if key.startField, key.startChar, err = keyPosition(start, &key, false, &modified); err != nil {
		return key, false, err
	}
	if ranged {
		if key.endField, key.endChar, err = keyPosition(end, &key, true, &modified); err != nil {
			return key, false, err
		}
	}
	return key, modified, nil
}

func keyPosition(text string, key *sortKey, end bool, modified *bool) (int, int, error) {
	for len(text) > 0 && strings.IndexByte("bfhnrV", text[len(text)-1]) >= 0 {
		switch text[len(text)-1] {
		case 'b':
			if end {
				key.blankEnd = true
			} else {
				key.blankStart = true
			}
		case 'f':
			key.fold = true
		case 'r':
			key.reverse = true
		case 'n':
			key.kind = sortNumeric
		case 'h':
			key.kind = sortHuman
		case 'V':
			key.kind = sortVersion
		}
		*modified = true
		text = text[:len(text)-1]
	}
	fieldText, charText, hasChar := strings.Cut(text, ".")
	field, err := strconv.Atoi(fieldText)
	if err != nil || field < 1 || strings.HasPrefix(fieldText, "+") {
		return 0, 0, formatError("field")
	}
	char := 1
	if end {
		char = 0
	}
	if hasChar {
		if char, err = strconv.Atoi(charText); err != nil || char < 0 || (!end && char < 1) || strings.HasPrefix(charText, "+") {
			return 0, 0, formatError("character")
		}
	}
	return field, char, nil
}

// fieldBounds returns the [start, end) byte offsets of each field. Without
// -t a field is optional leading blanks followed by non-blanks.
func (o sortOrder) fieldBounds(record []byte, field int) (int, int, bool) {
	start := 0
	for n := 1; ; n++ {
		end := start
		if o.separator >= 0 {
			if i := bytes.IndexByte(record[start:], byte(o.separator)); i >= 0 {
				end = start + i
			} else {
				end = len(record)
			}
		} else {
			for end < len(record) && (record[end] == ' ' || record[end] == '\t') {
				end++
			}
			for end < len(record) && record[end] != ' ' && record[end] != '\t' {
				end++
			}
		}
		if n == field {
			return start, end, true
		}
		if end >= len(record) {
			return len(record), len(record), false
		}
		start = end
		if o.separator >= 0 {
			start++
		}
	}
}

func (o sortOrder) keyText(record []byte, key sortKey) []byte {
	fieldStart, fieldEnd, _ := o.fieldBounds(record, key.startField)
	if key.blankStart {
		fieldStart = skipBlanks(record, fieldStart, fieldEnd)
	}
	start := min(fieldStart+key.startChar-1, fieldEnd)
	end := len(record)
	if key.endField > 0 {
		endStart, endEnd, _ := o.fieldBounds(record, key.endField)
		end = endEnd
		if key.endChar > 0 {
			if key.blankEnd {
				endStart = skipBlanks(record, endStart, endEnd)
			}
			end = min(endStart+key.endChar, endEnd)
		}
	}
	if end < start {
		return nil
	}
	return record[start:end]
}

func skipBlanks(record []byte, from, to int) int {
	for from < to && (record[from] == ' ' || record[from] == '\t') {
		from++
	}
	return from
}

// compareKeys orders two records by their keys alone.
func (o sortOrder) compareKeys(a, b []byte) int {
	for _, key := range o.keys {
		left, right := o.keyText(a, key), o.keyText(b, key)
		var c int
		switch key.kind {
		case sortNumeric:
			c = compareDecimal(left, right)
		case sortHuman:
			c = compareHuman(left, right)
		case sortVersion:
			c = compareVersion(left, right)
		default:
			c = compareFolded(left, right, key.fold)
		}
		if key.reverse {
			c = -c
		}
		if c != 0 {
			return c
		}
	}
	return 0
}

// compare is the complete order, including the whole-record tie-break.
func (o sortOrder) compare(a, b []byte) int {
	if c := o.compareKeys(a, b); c != 0 || o.unique || o.stable {
		return c
	}
	c := bytes.Compare(a, b)
	if o.reverse {
		return -c
	}
	return c
}

// checkSorted reports the first record out of order (with -u, also the
// first repeated key) on stderr with status 1, as sort -c does.
func checkSorted(inv *invocation, order sortOrder, delimiter byte) error {
	operands := inv.operands()
	if len(operands) > 1 {
		return inv.usage(operands[1], "at most one input with -c")
	}
	sources, err := inv.openInputs(operands)
	if err != nil || len(sources) == 0 {
		inv.status = max(inv.status, 2)
		return err
	}
	defer closeSources(sources)
	records, err := inv.records(sources[0].reader, delimiter)
	if err != nil {
		return err
	}
	var previous []byte
	for line := 1; ; line++ {
		record, err := records.next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if line > 1 {
			c := order.compare(previous, record)
			if c > 0 || (order.unique && order.compareKeys(previous, record) == 0) {
				fmt.Fprintf(&inv.errors, "sort: %s:%d: disorder: %s\n", sources[0].name, line, record)
				inv.status = 1
				return nil
			}
		}
		if cap(previous) < len(record) {
			if err := inv.reserve(int64(len(record))); err != nil {
				return err
			}
		}
		previous = append(previous[:0], record...)
	}
}
