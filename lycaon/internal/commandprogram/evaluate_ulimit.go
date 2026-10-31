package commandprogram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ResourceLimit is one per-process limit in the resource's native unit
// (bytes, seconds, descriptors or processes). Unlimited is math.MaxUint64.
type ResourceLimit struct {
	Resource   string
	Soft, Hard uint64
}

// LimitCapability is what the host's resource-limit owner provides for one
// resource. The zero value is unsupported.
type LimitCapability uint8

const (
	// LimitQuery reports the host's current soft and hard limits.
	LimitQuery LimitCapability = 1 << iota
	// LimitSetSoft starts later children with a soft limit up to the hard one.
	LimitSetSoft
	// LimitSetHard starts later children with a lowered hard limit.
	LimitSetHard
)

// ResourceLimits is the host's resource-limit owner. Capability is the
// platform's declared fact for a resource; Read is called only for a
// resource whose capability includes LimitQuery.
type ResourceLimits interface {
	Capability(resource string) LimitCapability
	Read(resource string) (ResourceLimit, error)
}

type noResourceLimits struct{}

func (noResourceLimits) Capability(string) LimitCapability { return 0 }
func (noResourceLimits) Read(resource string) (ResourceLimit, error) {
	return ResourceLimit{}, fmt.Errorf("resource limit %s unavailable", resource)
}

// childLimits are the resource limits a frame's external children start
// with. They begin as the host's own and stay unpinned until the program
// uses ulimit; from then on every later external child receives exactly the
// limits ulimit reported or set, so a report is what the child sees. A
// limit never outlives the program invocation.
type childLimits struct {
	mu     sync.Mutex
	values map[string]ResourceLimit
}

func (l *childLimits) clone() *childLimits {
	out := &childLimits{}
	if l == nil {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.values) > 0 {
		out.values = make(map[string]ResourceLimit, len(l.values))
		for resource, limit := range l.values {
			out.values[resource] = limit
		}
	}
	return out
}

// pinned lists the limits ulimit fixed for this frame, in resource order.
func (l *childLimits) pinned() []ResourceLimit {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ResourceLimit, 0, len(l.values))
	for _, limit := range l.values {
		out = append(out, limit)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out
}

func (l *childLimits) get(read func(string) (ResourceLimit, error), resource string) (ResourceLimit, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit, ok := l.values[resource]; ok {
		return limit, nil
	}
	limit, err := read(resource)
	if err != nil {
		return ResourceLimit{}, err
	}
	if l.values == nil {
		l.values = map[string]ResourceLimit{}
	}
	l.values[resource] = limit
	return limit, nil
}

func (l *childLimits) set(limit ResourceLimit) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values[limit.Resource] = limit
}

// ulimitResource is one resource in bash's report order with its units and
// the resource-limit owner's name for it.
type ulimitResource struct {
	option      byte
	resource    string
	description string
	units       string
	factor      uint64
}

var ulimitResources = []ulimitResource{
	{'c', "core", "core file size", "blocks", 1024},
	{'f', "fsize", "file size", "blocks", 1024},
	{'n', "nofile", "open files", "", 1},
	{'s', "stack", "stack size", "kbytes", 1024},
	{'t', "cpu", "cpu time", "seconds", 1},
	{'u', "nproc", "max user processes", "", 1},
	{'v', "as", "virtual memory", "kbytes", 1024},
}

// runUlimit reports and sets the limits later external children start with,
// from the capabilities the resource-limit owner declares. A value sets the
// soft limit up to the hard limit, with -H the hard limit and with -H -S
// both. Reading reports the soft limit unless -H is given alone.
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
	owner := f.e.ports.ResourceLimits
	if owner == nil {
		owner = noResourceLimits{}
	}
	all := len(bound.Options("-a")) > 0
	hardOption, hardFlag := bound.Last("-H")
	softFlag := len(bound.Options("-S")) > 0
	selected, unsupported := ulimitSelection(spec, bound, all, owner)
	if unsupported != nil {
		return ulimitRefusal(f, s, spec, unsupported)
	}
	// As in bash, a LIMIT sets the last named resource and the others print;
	// with -a, or past the first, operands are ignored.
	value, setSoft, setHard := "", !hardFlag || softFlag, hardFlag
	if operands := bound.Operands(); len(operands) > 0 && !all {
		value = operands[0].Value
		capability := owner.Capability(selected[len(selected)-1].resource)
		switch {
		case setHard && capability&LimitSetHard == 0 && capability&LimitSetSoft != 0:
			return ulimitRefusal(f, s, spec, &ArgvError{Code: argvOptionUnsupported, Command: spec.Name, Word: "-H", Expected: "ulimit [-S] -n LIMIT, which lowers the soft limit or raises it up to the hard limit", Span: hardOption.Span})
		case (setHard && capability&LimitSetHard == 0) || (setSoft && capability&LimitSetSoft == 0):
			return ulimitRefusal(f, s, spec, &ArgvError{Code: "COMMAND_PLATFORM_UNAVAILABLE", Command: spec.Name, Word: value, Expected: "a platform that sets this resource limit for launched programs", Span: operands[0].Span})
		}
	}
	readHard := hardFlag && !softFlag
	return f.inProcess(ctx, s, spec.Name, func(context.Context, toolcontract.OperationInvocation) (toolcontract.ProducerResult, error) {
		var out strings.Builder
		status := 0
		for i, resource := range selected {
			limit, err := f.state.limits.get(owner.Read, resource.resource)
			if err != nil {
				return ownerResult(err), err
			}
			switch {
			case value != "" && i == len(selected)-1:
				status = setUlimit(f, resource, limit, value, setSoft, setHard)
			case len(selected) == 1:
				out.WriteString(ulimitValue(limit, readHard, resource.factor) + "\n")
			default:
				unit := "(-" + string(resource.option) + ") "
				if resource.units != "" {
					unit = "(" + resource.units + ", -" + string(resource.option) + ") "
				}
				fmt.Fprintf(&out, "%-20s %20s%s\n", resource.description, unit, ulimitValue(limit, readHard, resource.factor))
			}
		}
		if _, err := io.WriteString(f.io.streams.Output, out.String()); err != nil {
			return ownerResult(err), err
		}
		return ordinaryResult(status), nil
	})
}

// ulimitSelection lists the named resources in argv order, every resource
// the owner can report for -a, and the file size when none is named. A named
// resource the owner cannot report is refused.
func ulimitSelection(spec toolcontract.CommandSpecification, bound BoundArgv, all bool, owner ResourceLimits) ([]ulimitResource, *ArgvError) {
	unsupported := func(word string, span toolcontract.SourceSpan) *ArgvError {
		return &ArgvError{Code: "COMMAND_PLATFORM_UNAVAILABLE", Command: spec.Name, Word: word, Expected: "a platform that reports this resource limit", Span: span}
	}
	var selected []ulimitResource
	if all {
		for _, resource := range ulimitResources {
			if owner.Capability(resource.resource)&LimitQuery != 0 {
				selected = append(selected, resource)
			}
		}
		return selected, nil
	}
	for _, item := range bound.Items {
		for _, resource := range ulimitResources {
			if item.Option == nil || item.Option.Key != "-"+string(resource.option) {
				continue
			}
			if owner.Capability(resource.resource)&LimitQuery == 0 {
				return nil, unsupported(item.Option.Key, item.Option.Span)
			}
			selected = append(selected, resource)
		}
	}
	if len(selected) == 0 {
		fileSize := ulimitResources[1]
		if owner.Capability(fileSize.resource)&LimitQuery == 0 {
			return nil, unsupported("-f", toolcontract.SourceSpan{})
		}
		selected = append(selected, fileSize)
	}
	return selected, nil
}

// setUlimit fixes new limits for later children. Bash's own errors keep
// their text and status 1.
func setUlimit(f *frame, resource ulimitResource, limit ResourceLimit, word string, setSoft, setHard bool) int {
	var value uint64
	ok := true
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
	if setHard && !setSoft && value < limit.Soft {
		fmt.Fprintf(f.io.streams.Error, "bash: ulimit: %s: cannot modify limit: Invalid argument\n", resource.description)
		return 1
	}
	if setHard {
		limit.Hard = value
	}
	if setSoft {
		limit.Soft = value
	}
	f.state.limits.set(limit)
	return 0
}

func ulimitValue(limit ResourceLimit, hard bool, factor uint64) string {
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
