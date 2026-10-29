package hostenv

import (
	"errors"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/hostos"
)

// system reads the declared platform facts.
func (inv *invocation) system() (hostos.System, error) {
	facts, err := inv.owners.Host.System(inv.ctx)
	if errors.Is(err, hostos.ErrUnsupported) {
		return facts, inv.unavailable("system identity", err)
	}
	return facts, err
}

func (inv *invocation) identity() (hostos.Identity, error) {
	facts, err := inv.owners.Host.Identity(inv.ctx)
	if errors.Is(err, hostos.ErrUnsupported) {
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
