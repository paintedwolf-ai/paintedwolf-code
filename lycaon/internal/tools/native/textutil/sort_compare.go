package textutil

import (
	"bytes"
	"strconv"
	"strings"
)

// sortKey is one -k START[,END]: one-based fields and characters (bytes).
// endField 0 runs to the end of the record; endChar 0 to the end of its field.
type sortKey struct {
	startField, startChar int
	endField, endChar     int
	numeric, reverse      bool
}

// sortOrder is the complete comparison: keys in order, then the whole record
// in byte order unless -u, where records equal on every key are duplicates.
// Ordering is unsigned byte order; there is no ambient locale.
type sortOrder struct {
	keys      []sortKey
	separator int
	reverse   bool
	unique    bool
}

func parseSortOrder(inv *invocation) (sortOrder, error) {
	order := sortOrder{separator: -1, reverse: inv.flag("-r"), unique: inv.flag("-u")}
	numeric := inv.flag("-n")
	if sep, ok := inv.value("-t"); ok {
		if len(sep.Value) != 1 {
			return order, inv.usage(sep, "a one-byte field separator")
		}
		order.separator = int(sep.Value[0])
	}
	for _, option := range inv.argv.Options("-k") {
		key, err := parseSortKey(option.Values[0].Value)
		if err != nil {
			return order, inv.usage(option.Values[0], "KEY as FIELD[.CHAR][n][r][,FIELD[.CHAR][n][r]]")
		}
		if !key.numeric && !key.reverse {
			key.numeric, key.reverse = numeric, order.reverse
		}
		order.keys = append(order.keys, key)
	}
	if len(order.keys) == 0 {
		order.keys = []sortKey{{startField: 1, startChar: 1, numeric: numeric, reverse: order.reverse}}
	}
	return order, nil
}

func parseSortKey(text string) (sortKey, error) {
	start, end, ranged := strings.Cut(text, ",")
	var key sortKey
	var err error
	if key.startField, key.startChar, err = keyPosition(start, &key, false); err != nil {
		return key, err
	}
	if ranged {
		if key.endField, key.endChar, err = keyPosition(end, &key, true); err != nil {
			return key, err
		}
	}
	return key, nil
}

func keyPosition(text string, key *sortKey, end bool) (int, int, error) {
	for len(text) > 0 && strings.IndexByte("nr", text[len(text)-1]) >= 0 {
		key.numeric = key.numeric || text[len(text)-1] == 'n'
		key.reverse = key.reverse || text[len(text)-1] == 'r'
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
	start := min(fieldStart+key.startChar-1, fieldEnd)
	end := len(record)
	if key.endField > 0 {
		endStart, endEnd, _ := o.fieldBounds(record, key.endField)
		end = endEnd
		if key.endChar > 0 {
			end = min(endStart+key.endChar, endEnd)
		}
	}
	if end < start {
		return nil
	}
	return record[start:end]
}

// compareKeys orders two records by their keys alone.
func (o sortOrder) compareKeys(a, b []byte) int {
	for _, key := range o.keys {
		left, right := o.keyText(a, key), o.keyText(b, key)
		var c int
		if key.numeric {
			c = compareDecimal(left, right)
		} else {
			c = bytes.Compare(left, right)
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
	if c := o.compareKeys(a, b); c != 0 || o.unique {
		return c
	}
	c := bytes.Compare(a, b)
	if o.reverse {
		return -c
	}
	return c
}

// compareDecimal compares leading [blanks][-]digits[.digits] exactly, with no
// float rounding; text without a number is zero.
func compareDecimal(a, b []byte) int {
	an, ai, af := decimalParts(a)
	bn, bi, bf := decimalParts(b)
	if an != bn {
		if an {
			return -1
		}
		return 1
	}
	c := len(ai) - len(bi)
	if c == 0 {
		c = bytes.Compare(ai, bi)
	}
	if c == 0 {
		c = bytes.Compare(af, bf)
	}
	if c > 0 {
		c = 1
	} else if c < 0 {
		c = -1
	}
	if an {
		return -c
	}
	return c
}

// decimalParts returns the sign, integer digits without leading zeros and
// fraction digits without trailing zeros; zero is never negative.
func decimalParts(text []byte) (negative bool, integer, fraction []byte) {
	i := 0
	for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
		i++
	}
	if i < len(text) && text[i] == '-' {
		negative = true
		i++
	}
	start := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	integer = bytes.TrimLeft(text[start:i], "0")
	if i < len(text) && text[i] == '.' {
		i++
		start = i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		fraction = bytes.TrimRight(text[start:i], "0")
	}
	if len(integer) == 0 && len(fraction) == 0 {
		negative = false
	}
	return negative, integer, fraction
}
