package hostenv

import (
	"errors"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// visibleEnvironment is the program's exported environment. Its starting
// values came from the same final environment constructor as child launches.
func (inv *invocation) visibleEnvironment() (map[string]string, error) {
	launch := exec.LaunchPlan{}
	if inv.bound != nil {
		launch = launch.WithTrustedEnvironment(inv.bound.BaseEnvironment())
	}
	if _, err := exec.AgentLaunchOptions(launch, "", inv.request.Environment, inv.tc.Host.HostResourcePathExtra); err != nil {
		return nil, inv.usage(inv.request.Argv[0], "an environment without host-reserved keys")
	}
	environment := inv.request.Environment
	visible := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		visible[name] = value
	}
	return visible, nil
}

// list emits NAME=VALUE lines in byte order.
func (inv *invocation) list(environment map[string]string) error {
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := inv.line(name + "=" + environment[name]); err != nil {
			return err
		}
	}
	return nil
}

// runEnv applies -i, -u NAME and NAME=VALUE operands to a copy of the
// request's environment, then lists it or runs COMMAND with exactly it.
// Assignments obey the launch's inline-key rules: host-reserved keys refuse.
func runEnv(inv *invocation) error {
	environment := map[string]string{}
	if !inv.flag("-i") {
		var err error
		environment, err = inv.visibleEnvironment()
		if err != nil {
			return err
		}
	}
	for _, option := range inv.argv.Options("-u") {
		for _, name := range option.Values {
			if !commandprogram.VariableName(name.Value) {
				return inv.usage(name, "a variable name for -u")
			}
			delete(environment, name.Value)
		}
	}
	operands := inv.operands()
	assignments := map[string]string{}
	next := 0
	for ; next < len(operands); next++ {
		name, value, assigned := strings.Cut(operands[next].Value, "=")
		if !assigned {
			break
		}
		if !commandprogram.VariableName(name) {
			return inv.usage(operands[next], "NAME=VALUE with a variable name")
		}
		if err := exec.ValidateInlineEnv(map[string]string{name: value}); err != nil {
			if errors.Is(err, exec.ErrBlockedEnvKey) {
				return errors.Join(err, inv.usage(operands[next], "a key the host does not reserve"))
			}
			return errors.Join(err, inv.usage(operands[next], "NAME=VALUE with a variable name"))
		}
		assignments[name] = value
	}
	for name, value := range assignments {
		environment[name] = value
	}
	if next == len(operands) {
		return inv.list(environment)
	}
	if inv.nested == nil {
		return nativeutility.Unsupported(inv.spec, operands[next], "a program runtime that dispatches env COMMAND")
	}
	result, err := inv.nested.RunNested(inv.ctx, inv.request, operands[next:], environment, inv.streams)
	inv.passthrough = &result
	return err
}

// runPrintenv lists the environment, or prints each named value; a missing
// name is status 1 and the rest still print.
func runPrintenv(inv *invocation) error {
	environment, err := inv.visibleEnvironment()
	if err != nil {
		return err
	}
	operands := inv.operands()
	if len(operands) == 0 {
		return inv.list(environment)
	}
	for _, name := range operands {
		value, ok := environment[name.Value]
		if !ok {
			inv.status = 1
			continue
		}
		if err := inv.line(value); err != nil {
			return err
		}
	}
	return nil
}
