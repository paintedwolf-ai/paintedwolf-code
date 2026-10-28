package commandprogram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// childLimits are the resource limits a frame's external children start
// with. They begin as the host's own and stay unpinned until the program
// uses ulimit; from then on every later external child receives exactly the
// limits ulimit reported or set, so a report is what the child sees. A
// limit never outlives the program invocation.
type childLimits struct {
	mu     sync.Mutex
	values map[string]confine.ResourceLimit
}

func (l *childLimits) clone() *childLimits {
	out := &childLimits{}
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.values) > 0 {
		out.values = make(map[string]confine.ResourceLimit, len(l.values))
		for resource, limit := range l.values {
			out.values[resource] = limit
		}
	}
	return out
}

// pinned lists the limits ulimit fixed for this frame, in resource order.
func (l *childLimits) pinned() []confine.ResourceLimit {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]confine.ResourceLimit, 0, len(l.values))
	for _, limit := range l.values {
		out = append(out, limit)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out
}

func (l *childLimits) get(read func(string) (confine.ResourceLimit, error), resource string) (confine.ResourceLimit, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit, ok := l.values[resource]; ok {
		return limit, nil
	}
	limit, err := read(resource)
	if err != nil {
		return confine.ResourceLimit{}, err
	}
	if l.values == nil {
		l.values = map[string]confine.ResourceLimit{}
	}
	l.values[resource] = limit
	return limit, nil
}

func (l *childLimits) set(limit confine.ResourceLimit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values[limit.Resource] = limit
}

// ulimitResource is one resource in bash's report order with its units.
type ulimitResource struct {
	option      byte
	description string
	units       string
	factor      uint64
}

var ulimitResources = []ulimitResource{
	{'c', "core file size", "blocks", 1024},
	{'f', "file size", "blocks", 1024},
	{'n', "open files", "", 1},
	{'s', "stack size", "kbytes", 1024},
	{'t', "cpu time", "seconds", 1},
	{'u', "max user processes", "", 1},
	{'v', "virtual memory", "kbytes", 1024},
}

// runUlimit reports or lowers the limits later external children start with.
// Without -H a value sets the soft limit, at most the hard limit; -H changes
// are refused. Reading uses the soft limit unless -H is given.
func runUlimit(ctx context.Context, f *frame, s commandsurface.Statement, fields Fields) error {
	spec, ok, err := ResolveCommand(fields)
	if err != nil || !ok {
		return errors.Join(err, fmt.Errorf("ulimit is missing from the language catalog"))
	}
	bound, err := f.e.ports.Binder.Bind(spec, fields)
	var argv *ArgvError
	if errors.As(err, &argv) {
		return ulimitRefusal(f, s, spec, argv)
	} else if err != nil {
		return err
	}
	if bound.Help {
		return f.inProcess(ctx, s, spec.Name, func(context.Context, toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
			return WriteCatalogHelp(spec, f.io.streams.Output)
		})
	}
	all := len(bound.Options("-a")) > 0
	selected := ulimitSelection(bound, all)
	hard := len(bound.Options("-H")) > 0 && len(bound.Options("-S")) == 0
	// As in bash, a LIMIT sets the last named resource and the others print;
	// with -a, or past the first, operands are ignored.
	value := ""
	if operands := bound.Operands(); len(operands) > 0 && !all {
		value = operands[0].Value
		if hard {
			option, _ := bound.Last("-H")
			return ulimitRefusal(f, s, spec, &ArgvError{Code: argvOptionUnsupported, Command: spec.Name, Word: "-H", Expected: "ulimit [-S] -n LIMIT, which lowers the soft limit or raises it up to the hard limit", Span: option.Span})
		}
		if runtime.GOOS == "windows" {
			return ulimitRefusal(f, s, spec, &ArgvError{Code: "COMMAND_PLATFORM_UNAVAILABLE", Command: spec.Name, Word: value, Expected: "a platform with per-process resource limits", Span: operands[0].Span})
		}
	}
	read := f.e.ports.ResourceLimits
	if read == nil {
		read = confine.ReadResourceLimit
	}
	return f.inProcess(ctx, s, spec.Name, func(context.Context, toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
		var out strings.Builder
		status := 0
		for i, resource := range selected {
			limit, err := f.state.limits.get(read, confine.LimitResources[resource.option])
			if err != nil {
				return ownerResult(err), err
			}
			switch {
			case value != "" && i == len(selected)-1:
				status = setUlimit(f, resource, limit, value)
			case len(selected) == 1:
				out.WriteString(ulimitValue(limit, hard, resource.factor) + "\n")
			default:
				unit := "(-" + string(resource.option) + ") "
				if resource.units != "" {
					unit = "(" + resource.units + ", -" + string(resource.option) + ") "
				}
				fmt.Fprintf(&out, "%-20s %20s%s\n", resource.description, unit, ulimitValue(limit, hard, resource.factor))
			}
		}
		if _, err := io.WriteString(f.io.streams.Output, out.String()); err != nil {
			return ownerResult(err), err
		}
		return ordinaryResult(status), nil
	})
}

// ulimitSelection lists the named resources in argv order, every resource
// for -a, and the file size when none is named.
func ulimitSelection(bound BoundArgv, all bool) []ulimitResource {
	if all {
		return ulimitResources
	}
	var selected []ulimitResource
	for _, item := range bound.Items {
		for _, resource := range ulimitResources {
			if item.Option != nil && item.Option.Key == "-"+string(resource.option) {
				selected = append(selected, resource)
			}
		}
	}
	if len(selected) == 0 {
		return ulimitResources[1:2]
	}
	return selected
}

// setUlimit fixes a new soft limit for later children. Bash's own errors
// keep their text and status 1.
func setUlimit(f *frame, resource ulimitResource, limit confine.ResourceLimit, word string) int {
	value, ok := uint64(0), true
	switch word {
	case "unlimited":
		value = math.MaxUint64
	case "hard":
		value = limit.Hard
	case "soft":
		value = limit.Soft
	default:
		n, err := strconv.ParseUint(word, 10, 64)
		ok = err == nil && n <= math.MaxUint64/resource.factor
		value = n * resource.factor
	}
	if !ok {
		fmt.Fprintf(f.io.streams.Error, "bash: ulimit: %s: invalid number\n", word)
		return 1
	}
	if value > limit.Hard {
		fmt.Fprintf(f.io.streams.Error, "bash: ulimit: %s: cannot modify limit: Operation not permitted\n", resource.description)
		return 1
	}
	limit.Soft = value
	f.state.limits.set(limit)
	return 0
}

func ulimitValue(limit confine.ResourceLimit, hard bool, factor uint64) string {
	value := limit.Soft
	if hard {
		value = limit.Hard
	}
	if value == math.MaxUint64 {
		return "unlimited"
	}
	return strconv.FormatUint(value/factor, 10)
}

func ulimitRefusal(f *frame, s commandsurface.Statement, spec toolcontract.CommandSpecification, argv *ArgvError) error {
	f.hostRefusal(spec.Name, argv.Word, argv.Expected, argv.Code)
	loc := s.SyntaxLocation
	if argv.Span != (toolcontract.SourceSpan{}) {
		loc.Span = argv.Span
	}
	refusal := f.refuse(loc, argv.Code, argv.Expected, argv)
	refusal.Command, refusal.Option, refusal.Form = spec.Name, argv.Word, spec.Argv
	return refusal
}
