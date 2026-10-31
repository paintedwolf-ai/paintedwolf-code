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
	var metrics survey.CountMetrics
	for flag, metric := range map[string]survey.CountMetrics{
		"-l": survey.CountLines, "-w": survey.CountWords,
		"-m": survey.CountChars, "-L": survey.CountDisplayWidth,
	} {
		if selected[flag] {
			metrics |= metric
		}
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
		in, err := inv.openField(operand, false)
		if err != nil {
			return err
		}
		if in == nil {
			continue
		}
		counter := survey.NewCounter(metrics)
		if in.stream != nil {
			if _, err := io.Copy(&counter, in.stream); err != nil {
				return err
			}
		} else {
			_, _ = counter.Write(in.data)
		}
		counts := counter.Counts()
		counts.MaxLineLength = counter.DisplayWidth()
		if scalars && counts.InvalidEncoding {
			inv.fail(1, operand.Value, survey.ErrInvalidEncoding.Error())
			continue
		}
		label := ""
		if named {
			label = operand.Value
		}
		rows = append(rows, wcRow{counts: counts, label: label})
		total.Lines += counts.Lines
		total.Words += counts.Words
		total.Chars += counts.Chars
		total.Bytes += counts.Bytes
		total.MaxLineLength = max(total.MaxLineLength, counts.MaxLineLength)
	}
	columns := 0
	for _, enabled := range selected {
		if enabled {
			columns++
		}
	}
	width := 1
	if len(inv.operands()) > 1 || columns > 1 {
		width = len(strconv.FormatInt(total.Bytes, 10))
		for _, operand := range inv.operandsOrStdin() {
			if operand.Value == "-" {
				width = max(width, 7)
			}
		}
	}
	for _, row := range rows {
		if err := inv.out.text(wcLine(row.counts, selected, width, row.label)); err != nil {
			return err
		}
	}
	if len(inv.operands()) > 1 {
		return inv.out.text(wcLine(total, selected, width, "total"))
	}
	return nil
}

func wcLine(counts survey.TextCounts, selected map[string]bool, width int, label string) string {
	var fields []string
	for _, column := range wcColumns {
		if selected[column.key] {
			fields = append(fields, fmt.Sprintf("%*d", width, column.value(counts)))
		}
	}
	if label != "" {
		fields = append(fields, label)
	}
	return strings.Join(fields, " ") + "\n"
}
