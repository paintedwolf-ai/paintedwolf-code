package hostenv

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools/native/datetext"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// runDate prints one instant. The clock is read once; -d STRING, -r FILE or
// SECONDS, or -f FORMAT STRING replaces it, each -v adjustment then applies
// in order, and the output is +FORMAT, -I, -R, --rfc-3339 or the default
// format. Times are in the program's TZ (UTC when unset) or UTC under -u.
// The clock is never set, so -j only selects the -f form.
func runDate(inv *invocation) error {
	loc := datetext.Zone(inv.request.Environment)
	if inv.flag("-u") {
		loc = time.UTC
	}
	format, input, err := inv.dateOperands()
	if err != nil {
		return err
	}
	at, ok, err := inv.dateInstant(inv.owners.Clock().In(loc), input)
	if err != nil || !ok {
		return err
	}
	for _, option := range inv.argv.Options("-v") {
		adjusted, err := datetext.Adjust(at, option.Values[0].Value)
		if err != nil {
			fmt.Fprintf(&inv.errors, "%s: Cannot apply date adjustment\n", option.Values[0].Value)
			inv.status = 1
			//nolint:nilerr // Invalid date adjustment reports status 1 on stderr.
			return nil
		}
		at = adjusted
	}
	return inv.line(datetext.Format(at, format))
}

// dateOperands splits the operands into the +FORMAT output form and the -f
// date string, and reads the output-format options.
func (inv *invocation) dateOperands() (string, *commandprogram.Field, error) {
	format, plus := "", false
	var input *commandprogram.Field
	for _, operand := range inv.operands() {
		switch {
		case strings.HasPrefix(operand.Value, "+") && !plus:
			format, plus = operand.Value[1:], true
		case input == nil && !plus && inv.flag("-f"):
			field := operand
			input = &field
		default:
			return "", nil, inv.usage(operand, "+FORMAT, or -f FORMAT STRING; setting the clock is not supported")
		}
	}
	if inv.flag("-f") && input == nil {
		return "", nil, inv.usage(inv.request.Argv[0], "a date STRING for -f FORMAT")
	}
	outputs := 0
	if plus {
		outputs++
	}
	if option, ok := inv.argv.Last("-I"); ok {
		outputs++
		value := "date"
		if len(option.Values) > 0 {
			value = option.Values[0].Value
		}
		layout, ok := isoLayout(value)
		if !ok {
			return "", nil, inv.usage(option.Values[0], "-I date, hours, minutes, seconds or ns")
		}
		format = layout
	}
	if option, ok := inv.argv.Last("--rfc-3339"); ok {
		outputs++
		layout, ok := map[string]string{"date": "%Y-%m-%d", "seconds": "%Y-%m-%d %H:%M:%S%:z", "ns": "%Y-%m-%d %H:%M:%S.%N%:z"}[option.Values[0].Value]
		if !ok {
			return "", nil, inv.usage(option.Values[0], "--rfc-3339 date, seconds or ns")
		}
		format = layout
	}
	if inv.flag("-R") {
		outputs++
		format = "%a, %d %b %Y %H:%M:%S %z"
	}
	if outputs > 1 {
		return "", nil, inv.usage(inv.request.Argv[0], "one output format")
	}
	if outputs == 0 {
		format = datetext.Default
	}
	return format, input, nil
}

// isoLayout reads an -I precision; any unambiguous prefix names it.
func isoLayout(value string) (string, bool) {
	for _, c := range []struct{ name, layout string }{
		{"date", "%Y-%m-%d"},
		{"hours", "%Y-%m-%dT%H%:z"},
		{"minutes", "%Y-%m-%dT%H:%M%:z"},
		{"seconds", "%Y-%m-%dT%H:%M:%S%:z"},
		{"ns", "%Y-%m-%dT%H:%M:%S,%N%:z"},
	} {
		if value != "" && strings.HasPrefix(c.name, value) {
			return c.layout, true
		}
	}
	return "", false
}

// dateInstant is the instant to print. ok is false after an ordinary
// failure already reported on stderr.
func (inv *invocation) dateInstant(now time.Time, input *commandprogram.Field) (time.Time, bool, error) {
	sources := 0
	for _, key := range []string{"-d", "-r", "-f"} {
		if inv.flag(key) {
			sources++
		}
	}
	if sources > 1 {
		return now, false, inv.usage(inv.request.Argv[0], "one of -d, -r and -f; the options to specify dates for printing are mutually exclusive")
	}
	if option, ok := inv.argv.Last("-d"); ok {
		at, err := datetext.Parse(option.Values[0].Value, now, now.Location())
		if err != nil {
			fmt.Fprintf(&inv.errors, "date: invalid date '%s'\n", option.Values[0].Value)
			inv.status = 1
			//nolint:nilerr // Invalid -d operand reports status 1 on stderr.
			return now, false, nil
		}
		return at, true, nil
	}
	if option, ok := inv.argv.Last("-r"); ok {
		return inv.referenceTime(now, option.Values[0].Value)
	}
	if option, ok := inv.argv.Last("-f"); ok {
		format := option.Values[0].Value
		at, rest, err := datetext.ParseFormat(format, input.Value, now)
		if err != nil {
			fmt.Fprintf(&inv.errors, "Failed conversion of ``%s'' using format ``%s''\ndate: illegal time format\n", input.Value, format)
			inv.status = 1
			//nolint:nilerr // Invalid -f conversion reports status 1 on stderr.
			return now, false, nil
		}
		if rest != "" {
			fmt.Fprintf(&inv.errors, "Warning: Ignoring %d extraneous characters in date string (%s)\n", len(rest), rest)
		}
		return at, true, nil
	}
	return now, true, nil
}

// referenceTime reads -r: a file's modification time through read authority,
// or, when no such file exists, a count of seconds since the epoch.
func (inv *invocation) referenceTime(now time.Time, value string) (time.Time, bool, error) {
	paths, err := projectpaths.NewProgramPaths(inv.ctx, inv.owners.Boundary, inv.tc, inv.request.Invocation.Scope.Directory)
	if err != nil {
		return now, false, err
	}
	bound, err := paths.Bind(inv.ctx, value, false)
	if err == nil {
		stat, statErr := bound.Stat(true)
		if statErr == nil {
			return stat.ModTime.In(now.Location()), true, nil
		}
		err = statErr
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return now, false, projectpaths.PathOperationError(value, err)
	}
	if seconds, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil {
		return time.Unix(seconds, 0).In(now.Location()), true, nil
	}
	fmt.Fprintf(&inv.errors, "date: %s: No such file or directory\n", value)
	inv.status = 1
	return now, false, nil
}
