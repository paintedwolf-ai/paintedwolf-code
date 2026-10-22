package literal

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/tools/native/survey"
)

// wcColumns are wc's selections in its fixed output order.
var wcColumns = []struct {
	key   string
	value func(survey.TextCounts) int64
}{
	{"-l", func(c survey.TextCounts) int64 { return c.Lines }},
	{"-w", func(c survey.TextCounts) int64 { return c.Words }},
	{"-m", func(c survey.TextCounts) int64 { return c.Chars }},
	{"-c", func(c survey.TextCounts) int64 { return c.Bytes }},
	{"-L", func(c survey.TextCounts) int64 { return c.MaxLineLength }},
}

func runWc(inv *invocation) error {
	selected := map[string]bool{}
	for _, column := range wcColumns {
		selected[column.key] = inv.flag(column.key)
	}
	if !selected["-l"] && !selected["-w"] && !selected["-m"] && !selected["-c"] && !selected["-L"] {
		selected["-l"], selected["-w"], selected["-c"] = true, true, true
	}
	scalars := selected["-m"] || selected["-L"]
	named := len(inv.operands()) > 0
	type wcRow struct {
		counts survey.TextCounts
		label  string
	}
	var rows []wcRow
	var total survey.TextCounts
	for _, operand := range inv.operandsOrStdin() {
		in, err := inv.open(operand, false)
		if err != nil {
			return err
		}
		if in == nil {
			continue
		}
		var counts survey.TextCounts
		if in.stream != nil {
			var counter survey.Counter
			if _, err := io.Copy(&counter, in.stream); err != nil {
				return err
			}
			counts = counter.Counts()
		} else {
			counts = survey.CountBytes(in.data)
		}
		if scalars && counts.InvalidEncoding {
			inv.fail(1, operand, survey.ErrInvalidEncoding.Error())
			continue
		}
		label := ""
		if named {
			label = operand
		}
		rows = append(rows, wcRow{counts: counts, label: label})
		total.Lines += counts.Lines
		total.Words += counts.Words
		total.Chars += counts.Chars
		total.Bytes += counts.Bytes
		total.MaxLineLength = max(total.MaxLineLength, counts.MaxLineLength)
	}
	var widths map[string]int
	if len(inv.operands()) > 1 {
		widths = map[string]int{}
		for _, column := range wcColumns {
			if selected[column.key] {
				widths[column.key] = len(strconv.FormatInt(column.value(total), 10))
			}
		}
	}
	for _, row := range rows {
		if err := inv.out.text(wcLine(row.counts, selected, widths, row.label)); err != nil {
			return err
		}
	}
	if len(inv.operands()) > 1 {
		return inv.out.text(wcLine(total, selected, widths, "total"))
	}
	return nil
}

func wcLine(counts survey.TextCounts, selected map[string]bool, widths map[string]int, label string) string {
	var fields []string
	for _, column := range wcColumns {
		if selected[column.key] {
			val := column.value(counts)
			if width := widths[column.key]; width > 0 {
				fields = append(fields, fmt.Sprintf("%*d", width, val))
			} else {
				fields = append(fields, strconv.FormatInt(val, 10))
			}
		}
	}
	if label != "" {
		fields = append(fields, label)
	}
	return strings.Join(fields, " ") + "\n"
}
