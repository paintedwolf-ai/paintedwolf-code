package literal

import (
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
		if err := inv.out.text(wcLine(counts, selected, label)); err != nil {
			return err
		}
		total.Lines += counts.Lines
		total.Words += counts.Words
		total.Chars += counts.Chars
		total.Bytes += counts.Bytes
		total.MaxLineLength = max(total.MaxLineLength, counts.MaxLineLength)
	}
	if len(inv.operands()) > 1 {
		return inv.out.text(wcLine(total, selected, "total"))
	}
	return nil
}

func wcLine(counts survey.TextCounts, selected map[string]bool, label string) string {
	var fields []string
	for _, column := range wcColumns {
		if selected[column.key] {
			fields = append(fields, strconv.FormatInt(column.value(counts), 10))
		}
	}
	if label != "" {
		fields = append(fields, label)
	}
	return strings.Join(fields, " ") + "\n"
}
