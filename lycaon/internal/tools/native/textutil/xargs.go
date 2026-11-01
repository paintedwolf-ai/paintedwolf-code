package textutil

import (
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// runXargs reads literal items incrementally and dispatches bounded batches.
func runXargs(inv *invocation) error {
	x := xargsRun{inv: inv}
	plan, err := x.plan()
	if err != nil {
		return err
	}
	input, err := x.input(plan)
	if err != nil {
		return err
	}
	return x.execute(input, plan)
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
