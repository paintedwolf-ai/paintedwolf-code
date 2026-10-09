package native

import (
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
)

// rejectCommandNotFound maps unresolved executable failures into agent-facing rejects.
func rejectCommandNotFound(res *hostcmd.Result, args map[string]any) error {
	if res == nil || !confine.ReportsCommandNotFound(res.ExitCode, res.Tail) {
		return nil
	}
	return commandNotFoundReject(commandArgv0(args))
}

func commandNotFoundReject(name string) error {
	name = strings.TrimSpace(name)
	data := map[string]any{}
	if name != "" {
		data["command"] = name
		if near := resolvableNeighbours(name); len(near) > 0 {
			data["resolvable"] = strings.Join(near, ", ")
		}
	}
	return &toolrejection.ToolReject{Code: toolrejection.CommandNotFoundCode, Data: data}
}

// neighbourLimit bounds the reject.
const neighbourLimit = 4

// resolvableNeighbours finds executables on the resolved PATH commands run
// with that are prefixed by the unresolved command.
func resolvableNeighbours(name string) []string {
	if name == "" || strings.ContainsRune(name, os.PathSeparator) {
		return nil
	}
	seen := map[string]struct{}{name: {}}
	var out []string
	for _, dir := range filepath.SplitList(exec.EffectivePathValue()) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			candidate := entry.Name()
			if !strings.HasPrefix(candidate, name) || candidate == name {
				continue
			}
			if _, dup := seen[candidate]; dup {
				continue
			}
			if _, err := exec.LookPathIn(candidate, dir); err != nil {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	sort.Strings(out)
	if len(out) > neighbourLimit {
		out = out[:neighbourLimit]
	}
	return out
}

func commandArgv0(args map[string]any) string {
	plan, err := commandsurface.ParsePlan(args)
	if err == nil && len(plan.Stages) > 0 {
		if name := strings.TrimSpace(plan.Stages[0].Name); name != "" {
			return name
		}
	}
	line := commandsurface.PrimaryCommandLine(args, nil)
	if line == "" {
		return ""
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func rejectStartCommandNotFound(err error) error {
	if err == nil {
		return nil
	}
	if confine.IsCommandNotFound(err) {
		var nf *confine.CommandNotFoundError
		if errors.As(err, &nf) && nf != nil {
			return commandNotFoundReject(nf.Name)
		}
		return commandNotFoundReject("")
	}
	var pathErr *osexec.Error
	if errors.As(err, &pathErr) && pathErr != nil && errors.Is(pathErr.Err, osexec.ErrNotFound) {
		return commandNotFoundReject(pathErr.Name)
	}
	if errors.Is(err, osexec.ErrNotFound) {
		return commandNotFoundReject("")
	}
	return nil
}
