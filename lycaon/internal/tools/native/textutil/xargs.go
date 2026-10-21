package textutil

import (
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// runXargs reads all of standard input, splits it into items and runs
// COMMAND (default echo) in batches, in input order, as GNU xargs does. Each
// batch is one child through the evaluator's nested-command port, with its
// own execution identity, binding and receipt; no shell runs and no input
// becomes program text. Empty input runs COMMAND once with no items unless
// -r or -I. With -P, up to that many batches (at most concurrent_processes)
// run at once and their output is delivered in input order.
func runXargs(inv *invocation) error {
	x := xargsRun{inv: inv}
	plan, err := x.plan()
	if err != nil {
		return err
	}
	input, err := inv.readAll(inv.streams.Input)
	if err != nil {
		return err
	}
	lines, err := x.lines(input, plan.replacing, plan.eof)
	if err != nil {
		return err
	}
	command := inv.operands()
	if len(command) == 0 {
		command = commandprogram.Fields{{Value: "echo", Span: inv.request.Invocation.Span}}
	}
	batches, err := plan.batches(inv, command, lines)
	if err != nil {
		return err
	}
	if len(batches) == 0 && !plan.replacing && !inv.flag("-r") {
		batches = []commandprogram.Fields{command}
	}
	if len(batches) == 0 {
		return nil
	}
	if inv.ports.Nested == nil {
		return unsupported(inv, command[0], "a program runtime that dispatches xargs batches")
	}
	return x.execute(batches, plan.parallel)
}

// xargsRun is one xargs invocation's input splitting and batch planning.
type xargsRun struct{ inv *invocation }

// xargsPlan is the batching the options select: count items or lines per
// batch (0 fills by size), one line per batch with replacing (-I, GNU -i),
// BSD -J's argument that the items replace, the logical EOF item (-E, GNU
// -e), the command-line byte cap (-s) and whether exceeding it refuses
// (-x), and parallel batches at once.
type xargsPlan struct {
	count, lines, parallel int
	placeholder            commandprogram.Field
	replacing              bool
	join                   string
	eof                    *string
	maxChars               int64
	exact                  bool
}

func (x xargsRun) plan() (xargsPlan, error) {
	plan := xargsPlan{parallel: 1, maxChars: x.inv.limit("argv_bytes"), exact: x.inv.flag("-x")}
	var err error
	if plan.count, err = x.positive("-n"); err != nil {
		return plan, err
	}
	if plan.lines, err = x.positive("-L"); err != nil {
		return plan, err
	}
	if option, ok := x.inv.argv.Last("-l"); ok {
		plan.lines = 1
		if len(option.Values) > 0 {
			if plan.lines, err = x.count(option.Values[0]); err != nil {
				return plan, err
			}
		}
	}
	plan.placeholder, plan.replacing = x.inv.value("-I")
	if option, ok := x.inv.argv.Last("-i"); ok && !plan.replacing {
		plan.placeholder, plan.replacing = commandprogram.Field{Value: "{}", Span: option.Span}, true
		if len(option.Values) > 0 {
			plan.placeholder = option.Values[0]
		}
	}
	if value, ok := x.inv.value("-J"); ok {
		if plan.replacing || value.Value == "" {
			return plan, x.inv.usage(value, "a nonempty -J placeholder, without -I")
		}
		plan.join = value.Value
	}
	if value, ok := x.inv.value("-E"); ok {
		plan.eof = &value.Value
	} else if option, ok := x.inv.argv.Last("-e"); ok && len(option.Values) > 0 {
		plan.eof = &option.Values[0].Value
	}
	if value, ok := x.inv.value("-s"); ok {
		n, err := x.count(value)
		if err != nil {
			return plan, err
		}
		plan.maxChars = min(int64(n), plan.maxChars)
	}
	switch {
	case plan.count > 0 && plan.lines > 0:
		return plan, x.inv.usage(x.inv.commandWord(), "-n or -L, not both")
	case plan.replacing && (plan.count > 0 || plan.lines > 0):
		return plan, x.inv.usage(plan.placeholder, "-I alone, without -n or -L")
	case plan.replacing && plan.placeholder.Value == "":
		return plan, x.inv.usage(plan.placeholder, "a nonempty placeholder")
	}
	if value, ok := x.inv.value("-P"); ok {
		n, err := strconv.Atoi(value.Value)
		if err != nil || n < 0 || strings.HasPrefix(value.Value, "+") {
			return plan, x.inv.usage(value, "a process count from 0")
		}
		bound := int(x.inv.limit("concurrent_processes"))
		if n == 0 || n > bound {
			n = bound
		}
		plan.parallel = max(n, 1)
	}
	return plan, nil
}

func (x xargsRun) positive(key string) (int, error) {
	value, ok := x.inv.value(key)
	if !ok {
		return 0, nil
	}
	return x.count(value)
}

func (x xargsRun) count(value commandprogram.Field) (int, error) {
	count, err := strconv.Atoi(value.Value)
	if err != nil || count < 1 || strings.HasPrefix(value.Value, "+") {
		return 0, x.inv.usage(value, "a positive COUNT")
	}
	return count, nil
}

// lines splits input into its logical lines of items. -0 and -d give exact
// items, one per line; -I gives one item per nonempty line without leading
// blanks; otherwise blanks separate items, quotes group, a backslash quotes
// one byte, and a line ending in a blank continues on the next (GNU -L).
func (x xargsRun) lines(input []byte, replacing bool, eof *string) ([][]string, error) {
	if delimiter, exact, err := x.delimiter(); err != nil || exact {
		return exactItems(input, delimiter), err
	}
	if replacing {
		var out [][]string
		for _, line := range strings.Split(string(input), "\n") {
			if line = strings.TrimLeft(line, " \t"); line != "" {
				out = append(out, []string{line})
			}
		}
		return out, nil
	}
	var out [][]string
	var line []string
	var item strings.Builder
	present, quote, blankEnd, stopped := false, byte(0), false, false
	end := func() {
		if present {
			if eof != nil && item.String() == *eof {
				stopped = true
			} else {
				line = append(line, item.String())
			}
			item.Reset()
			present = false
		}
	}
	for i := 0; i < len(input) && !stopped; i++ {
		c := input[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			if c == '\n' {
				return nil, x.inv.usage(x.inv.commandWord(), "input with matched quotes on one line")
			}
			item.WriteByte(c)
		case c == '\'' || c == '"':
			quote, present, blankEnd = c, true, false
		case c == '\\' && i+1 < len(input):
			i++
			item.WriteByte(input[i])
			present, blankEnd = true, false
		case c == '\n':
			end()
			if !blankEnd && len(line) > 0 {
				out, line = append(out, line), nil
			}
		case c == ' ' || c == '\t':
			end()
			blankEnd = true
		default:
			item.WriteByte(c)
			present, blankEnd = true, false
		}
	}
	if quote != 0 && !stopped {
		return nil, x.inv.usage(x.inv.commandWord(), "input with matched quotes")
	}
	end()
	if len(line) > 0 {
		out = append(out, line)
	}
	return out, nil
}

// delimiter is the exact item terminator of -0 or -d, which accepts one byte
// or a C escape (\n, \t, \0, \\, \NNN octal, \xHH).
func (x xargsRun) delimiter() (byte, bool, error) {
	if x.inv.flag("-0") {
		return 0, true, nil
	}
	value, ok := x.inv.value("-d")
	if !ok {
		return 0, false, nil
	}
	text := value.Value
	if len(text) == 1 {
		return text[0], true, nil
	}
	if len(text) >= 2 && text[0] == '\\' {
		if text == `\0` {
			return 0, true, nil
		}
		if c, width, _ := trByte(text); width == len(text) {
			return c, true, nil
		}
		if text[1] == 'x' {
			if v, err := strconv.ParseUint(text[2:], 16, 8); err == nil && len(text) <= 4 {
				return byte(v), true, nil
			}
		}
	}
	return 0, false, x.inv.usage(value, "one delimiter byte or a C escape")
}

func exactItems(input []byte, delimiter byte) [][]string {
	if len(input) == 0 {
		return nil
	}
	items := strings.Split(string(input), string([]byte{delimiter}))
	if items[len(items)-1] == "" {
		items = items[:len(items)-1]
	}
	out := make([][]string, len(items))
	for i, item := range items {
		out[i] = []string{item}
	}
	return out
}
