package textutil

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"
)

// runColumn formats its input lines into columns as util-linux and BSD
// column do. -t aligns fields split on runs of -s characters (default
// blanks), padding all but the last column and joining with -o (default two
// spaces). Otherwise nonempty lines fill columns top to bottom (-x: rows
// left to right) within -c width (default 80), padded with tabs to a width
// rounded up to a tab stop.
func runColumn(inv *invocation) error {
	width := 80
	if value, ok := inv.value("-c"); ok {
		n, err := strconv.Atoi(value.Value)
		if err != nil || n < 1 {
			return inv.usage(value, "a positive WIDTH")
		}
		width = n
	}
	var lines [][]byte
	err := inv.eachRecord(func(record []byte, _ bool) error {
		if len(record) == 0 {
			return nil
		}
		if err := inv.reserve(int64(len(record))); err != nil {
			return err
		}
		lines = append(lines, bytes.Clone(record))
		return nil
	})
	if err != nil {
		return err
	}
	var out []byte
	if inv.flag("-t") {
		out = formatTable(inv, lines)
	} else {
		out = fillColumns(lines, width, inv.flag("-x"))
	}
	_, err = inv.out.Write(out)
	return err
}

func formatTable(inv *invocation, lines [][]byte) []byte {
	separators := " \t"
	if value, ok := inv.value("-s"); ok {
		separators = value.Value
	}
	gap := "  "
	if value, ok := inv.value("-o"); ok {
		gap = value.Value
	}
	rows := make([][]string, len(lines))
	var widths []int
	for i, line := range lines {
		rows[i] = strings.FieldsFunc(string(line), func(r rune) bool { return strings.ContainsRune(separators, r) })
		for c, cell := range rows[i] {
			if c == len(widths) {
				widths = append(widths, 0)
			}
			widths[c] = max(widths[c], utf8.RuneCountInString(cell))
		}
	}
	var out []byte
	for _, row := range rows {
		for c, cell := range row {
			out = append(out, cell...)
			if c < len(row)-1 {
				out = append(out, strings.Repeat(" ", widths[c]-utf8.RuneCountInString(cell))...)
				out = append(out, gap...)
			}
		}
		out = append(out, '\n')
	}
	return out
}

// fillColumns is BSD column's columnation, filling columns first or, with
// -x, rows first.
func fillColumns(lines [][]byte, width int, rowsFirst bool) []byte {
	longest := 0
	for _, line := range lines {
		longest = max(longest, utf8.RuneCount(line))
	}
	longest = (longest + 8) &^ 7
	var out []byte
	if len(lines) == 0 {
		return nil
	}
	if longest >= width {
		for _, line := range lines {
			out = append(append(out, line...), '\n')
		}
		return out
	}
	columns := width / longest
	rows := (len(lines) + columns - 1) / columns
	if rowsFirst {
		return fillRows(lines, longest, columns)
	}
	for row := 0; row < rows; row++ {
		end := longest
		count := 0
		for base := row; base < len(lines); base += rows {
			out = append(out, lines[base]...)
			count += utf8.RuneCount(lines[base])
			if base+rows >= len(lines) {
				break
			}
			for next := (count + 8) &^ 7; next <= end; next = (count + 8) &^ 7 {
				out = append(out, '\t')
				count = next
			}
			end += longest
		}
		out = append(out, '\n')
	}
	return out
}

// fillRows writes entries across each row, tab-padded to the column width.
func fillRows(lines [][]byte, longest, columns int) []byte {
	var out []byte
	for i, line := range lines {
		out = append(out, line...)
		if (i+1)%columns == 0 || i == len(lines)-1 {
			out = append(out, '\n')
			continue
		}
		for count := utf8.RuneCount(line); count < longest; count = (count + 8) &^ 7 {
			out = append(out, '\t')
		}
	}
	return out
}
