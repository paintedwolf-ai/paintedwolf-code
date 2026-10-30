package hostenv

import (
	"errors"
	"os/exec"

	"github.com/lycaon/lycaon/internal/confine"
	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// keywords are the version 1 reserved words; they are syntax, never programs.
var keywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true, "for": true, "in": true,
	"while": true, "do": true, "done": true, "!": true, "{": true, "}": true, "[[": true, "]]": true,
}

// discovery is how dispatch would bind one name. Native catalog names bind
// first, before any installed program; path is the program a confined launch
// would select, found or not, independently of a native binding.
type discovery struct {
	name    string
	native  bool
	keyword bool
	path    string
}

func (d discovery) found() bool { return d.native || d.keyword || d.path != "" }

// discover classifies one name. The external lookup is the launch's own
// selection: the same final PATH, launch directory and not-found result. A
// native name is looked up only when every binding is requested (type -a).
func (inv *invocation) discover(name string, every bool) (discovery, error) {
	d := discovery{name: name, native: inv.owners.natives[name] && inv.bound != nil && inv.bound.NativeBound(name), keyword: keywords[name]}
	if d.keyword || (d.native && !every) {
		return d, nil
	}
	cwd, _, err := projectpaths.CommandCwd(inv.ctx, inv.tc, inv.request.Invocation.Scope.Directory)
	if err != nil {
		return d, err
	}
	// These are the environment inputs of the confined external launch.
	opts, err := lyexec.AgentLaunchOptions(lyexec.LaunchPlan{}, cwd, inv.request.Environment, inv.tc.Host.HostResourcePathExtra, inv.tc.Files.PackageExecution != nil)
	path := ""
	if err == nil {
		path, err = lyexec.LaunchLookup(name, opts)
	}
	var missing *confine.CommandNotFoundError
	switch {
	case err == nil:
		d.path = path
	case errors.As(err, &missing), errors.Is(err, exec.ErrNotFound), errors.Is(err, lyexec.ErrNotInPath):
	case errors.Is(err, lyexec.ErrBlockedEnvKey):
		// The launch refuses this environment the same way.
		return d, errors.Join(err, inv.usage(inv.request.Argv[0], "an environment without host-reserved keys"))
	default:
		return d, err
	}
	return d, nil
}

// names discovers each operand in order; a refused lookup stops the run.
func (inv *invocation) names(visit func(discovery) error) error {
	every := inv.spec.Name == "type" && inv.flag("-a")
	for _, operand := range inv.operands() {
		d, err := inv.discover(operand.Value, every)
		if err != nil {
			return err
		}
		if !d.found() {
			inv.status = 1
		}
		if err := visit(d); err != nil {
			return err
		}
	}
	return nil
}

// runWhich prints NAME: native for a native binding, otherwise the program a
// launch would select. Keywords and unknown names are status 1.
func runWhich(inv *invocation) error {
	return inv.names(func(d discovery) error {
		switch {
		case d.native:
			return inv.line(d.name + ": native")
		case d.path != "":
			return inv.line(d.path)
		}
		inv.status = 1
		return nil
	})
}

// runCommand implements command -v (the name or program) and command -V (a
// description). Running COMMAND itself is not a version 1 form.
func runCommand(inv *invocation) error {
	verbose := inv.flag("-V")
	if !verbose && !inv.flag("-v") {
		return inv.usage(inv.request.Argv[0], "-v or -V")
	}
	return inv.names(func(d discovery) error {
		switch {
		case !d.found():
			if verbose {
				inv.fail("%s: not found", d.name)
			}
			return nil
		case verbose:
			return inv.line(describe(d))
		case d.native || d.keyword:
			return inv.line(d.name)
		}
		return inv.line(d.path)
	})
}

// runType describes each name; -t prints only its kind and -a every binding
// in dispatch order: the native built-in, then the program external -- runs.
func runType(inv *invocation) error {
	kinds, all := inv.flag("-t"), inv.flag("-a")
	return inv.names(func(d discovery) error {
		if !d.found() {
			if !kinds {
				inv.fail("%s: not found", d.name)
			}
			return nil
		}
		bindings := []discovery{d}
		if all && d.native && d.path != "" {
			bindings = []discovery{{name: d.name, native: true}, {name: d.name, path: d.path}}
		}
		for _, binding := range bindings {
			text := describe(binding)
			if kinds {
				text = kind(binding)
			}
			if err := inv.line(text); err != nil {
				return err
			}
		}
		return nil
	})
}

func describe(d discovery) string {
	switch {
	case d.keyword:
		return d.name + " is a shell keyword"
	case d.native:
		return d.name + " is a native built-in"
	}
	return d.name + " is " + d.path
}

func kind(d discovery) string {
	switch {
	case d.keyword:
		return "keyword"
	case d.native:
		return "builtin"
	}
	return "file"
}
