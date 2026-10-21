package hostenv

import (
	"errors"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// runPwd delegates to the path owner that also serves cd and native_path, in
// a frame bound to the request's directory; it prints the logical cwd.
func runPwd(inv *invocation) error {
	paths, err := projectpaths.NewProgramPaths(inv.ctx, inv.owners.Boundary, inv.tc, inv.request.Invocation.Scope.Directory)
	if err != nil {
		return err
	}
	_, err = paths.Invoke(inv.ctx, inv.request, commandprogram.Streams{Output: &inv.out})
	return err
}

func (inv *invocation) system() (SystemFacts, error) {
	facts, err := inv.owners.Host.System(inv.ctx)
	if errors.Is(err, ErrHostFactUnavailable) {
		return facts, inv.unavailable("system identity", err)
	}
	return facts, err
}

func (inv *invocation) identity() (IdentityFacts, error) {
	facts, err := inv.owners.Host.Identity(inv.ctx)
	if errors.Is(err, ErrHostFactUnavailable) {
		return facts, inv.unavailable("process identity", err)
	}
	return facts, err
}

// runUname prints the selected fields in the fixed s n r v m order; no flag
// selects -s and -a selects all five.
func runUname(inv *invocation) error {
	facts, err := inv.system()
	if err != nil {
		return err
	}
	all := inv.flag("-a")
	fields := []struct {
		flag, value string
	}{{"-s", facts.Sysname}, {"-n", facts.Nodename}, {"-r", facts.Release}, {"-v", facts.Version}, {"-m", facts.Machine}}
	var selected []string
	for _, field := range fields {
		if all || inv.flag(field.flag) {
			selected = append(selected, field.value)
		}
	}
	if len(selected) == 0 {
		selected = []string{facts.Sysname}
	}
	return inv.line(strings.Join(selected, " "))
}

func runHostname(inv *invocation) error {
	facts, err := inv.system()
	if err != nil {
		return err
	}
	return inv.line(facts.Nodename)
}

func runWhoami(inv *invocation) error {
	facts, err := inv.identity()
	if err != nil {
		return err
	}
	if facts.User == "" {
		return inv.unavailable("user name", nil)
	}
	return inv.line(facts.User)
}

// runID prints numeric uid=U gid=G groups=G,... by default; -u, -g and -G
// select one field, and -n prints those as names.
func runID(inv *invocation) error {
	facts, err := inv.identity()
	if err != nil {
		return err
	}
	var selected []int
	switch count := countFlags(inv, "-u", "-g", "-G"); {
	case count > 1:
		return inv.usage(inv.request.Argv[0], "at most one of -u, -g and -G")
	case count == 0 && inv.flag("-n"):
		return inv.usage(inv.request.Argv[0], "-n with -u, -g or -G")
	case count == 0:
		groups := make([]string, len(facts.Groups))
		for i, gid := range facts.Groups {
			groups[i] = strconv.Itoa(gid)
		}
		return inv.line("uid=" + strconv.Itoa(facts.UID) + " gid=" + strconv.Itoa(facts.GID) + " groups=" + strings.Join(groups, ","))
	case inv.flag("-u"):
		if inv.flag("-n") {
			if facts.User == "" {
				return inv.unavailable("user name", nil)
			}
			return inv.line(facts.User)
		}
		return inv.line(strconv.Itoa(facts.UID))
	case inv.flag("-g"):
		selected = []int{facts.GID}
	default:
		selected = facts.Groups
	}
	words := make([]string, len(selected))
	for i, gid := range selected {
		words[i] = strconv.Itoa(gid)
		if inv.flag("-n") {
			if words[i] = facts.GroupNames[gid]; words[i] == "" {
				return inv.unavailable("group name", nil)
			}
		}
	}
	return inv.line(strings.Join(words, " "))
}

func countFlags(inv *invocation, keys ...string) int {
	count := 0
	for _, key := range keys {
		if inv.flag(key) {
			count++
		}
	}
	return count
}

// runDate reads the clock once and prints UTC: YYYY-MM-DDTHH:MM:SSZ, or the
// single +FORMAT operand with %Y %m %d %H %M %S %s %z %% and literal text.
// -u is explicit UTC, which every form already is; there is no ambient locale.
func runDate(inv *invocation) error {
	operands := inv.operands()
	if len(operands) > 1 {
		return inv.usage(operands[1], "at most one +FORMAT operand")
	}
	format := "%Y-%m-%dT%H:%M:%SZ"
	if len(operands) == 1 {
		if !strings.HasPrefix(operands[0].Value, "+") {
			return inv.usage(operands[0], "+FORMAT; setting the clock is not supported")
		}
		format = operands[0].Value[1:]
	}
	text, err := formatDate(inv, operands, format)
	if err != nil {
		return err
	}
	return inv.line(text)
}

func formatDate(inv *invocation, operands commandprogram.Fields, format string) (string, error) {
	now := inv.owners.Clock().UTC()
	var out strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			out.WriteByte(format[i])
			continue
		}
		i++
		if i == len(format) {
			return "", inv.usage(operands[0], "a complete % directive")
		}
		switch format[i] {
		case 'Y':
			out.WriteString(pad(now.Year(), 4))
		case 'm':
			out.WriteString(pad(int(now.Month()), 2))
		case 'd':
			out.WriteString(pad(now.Day(), 2))
		case 'H':
			out.WriteString(pad(now.Hour(), 2))
		case 'M':
			out.WriteString(pad(now.Minute(), 2))
		case 'S':
			out.WriteString(pad(now.Second(), 2))
		case 's':
			out.WriteString(strconv.FormatInt(now.Unix(), 10))
		case 'z':
			out.WriteString("+0000")
		case '%':
			out.WriteByte('%')
		default:
			return "", inv.usage(operands[0], "%Y, %m, %d, %H, %M, %S, %s, %z or %%")
		}
	}
	return out.String(), nil
}

func pad(value, width int) string {
	text := strconv.Itoa(value)
	for len(text) < width {
		text = "0" + text
	}
	return text
}

// runNproc prints the available processing units (--all: installed) less
// --ignore N, never below one.
func runNproc(inv *invocation) error {
	ignore := 0
	if option, ok := inv.argv.Last("--ignore"); ok {
		n, err := strconv.Atoi(option.Values[0].Value)
		if err != nil || n < 0 {
			return inv.usage(option.Values[0], "a count from 0")
		}
		ignore = n
	}
	units, err := inv.owners.Host.Processors(inv.ctx, inv.flag("--all"))
	if err != nil {
		return inv.unavailable("processor count", err)
	}
	return inv.line(strconv.Itoa(max(units-ignore, 1)))
}
