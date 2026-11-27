package commandmode

import (
	"context"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type catalogLookup struct {
	specs           map[string]toolcontract.CommandSpecification
	twoWordByFamily map[string][]toolcontract.CommandSpecification
	declaredModes   map[string]map[string]bool
}

var loadCatalogLookup = sync.OnceValue(func() catalogLookup {
	out := catalogLookup{
		specs:           map[string]toolcontract.CommandSpecification{},
		twoWordByFamily: map[string][]toolcontract.CommandSpecification{},
		declaredModes:   map[string]map[string]bool{},
	}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return out
	}
	for _, spec := range catalog.Commands {
		out.specs[spec.Name] = spec
		for _, alias := range spec.Aliases {
			if _, taken := out.specs[alias]; !taken {
				out.specs[alias] = spec
			}
		}
		if parts := strings.Fields(spec.Name); len(parts) > 1 {
			out.twoWordByFamily[parts[0]] = append(out.twoWordByFamily[parts[0]], spec)
		}
		modes := make(map[string]bool, len(spec.Effects))
		for _, eff := range spec.Effects {
			if eff.Mode != "" {
				modes[eff.Mode] = true
			}
		}
		out.declaredModes[spec.Name] = modes
	}
	return out
})

// KnownMode reports whether mode is declared on command in the language catalog.
func KnownMode(command, mode string) bool {
	if command == "" || mode == "" {
		return false
	}
	return loadCatalogLookup().declaredModes[command][mode]
}

// Resolve binds words through the catalog grammar and selects the invocation's effect mode.
func Resolve(ctx context.Context, words []string) (BoundInvocation, bool) {
	if len(words) == 0 || strings.TrimSpace(words[0]) == "" {
		return BoundInvocation{}, false
	}
	idx := loadCatalogLookup()
	fields := make(commandprogram.Fields, len(words))
	for i, w := range words {
		fields[i] = commandprogram.Field{Value: w}
	}
	if candidates := idx.twoWordByFamily[words[0]]; len(candidates) > 0 && len(words) > 1 {
		if direct, ok := idx.specs[words[0]+" "+words[1]]; ok {
			if inv, valid := resolveSpec(ctx, direct, fields); valid {
				return inv, true
			}
		}
		for _, candidate := range candidates {
			if inv, valid := resolveSpec(ctx, candidate, fields); valid {
				return inv, true
			}
		}
		if words[0] == "git" {
			return BoundInvocation{}, false
		}
	}
	spec, ok := idx.specs[words[0]]
	if !ok {
		return BoundInvocation{}, false
	}
	return resolveSpec(ctx, spec, fields)
}

func resolveSpec(ctx context.Context, spec toolcontract.CommandSpecification, fields commandprogram.Fields) (BoundInvocation, bool) {
	if len(spec.Effects) == 0 {
		return BoundInvocation{}, false
	}
	bound, err := BindSearchArgv(spec, fields)
	if err != nil {
		return BoundInvocation{}, false
	}
	if len(spec.Effects) == 1 {
		mode := spec.Effects[0].Mode
		if !KnownMode(spec.Name, mode) {
			return BoundInvocation{}, false
		}
		return BoundInvocation{Command: spec.Name, Mode: mode}, true
	}
	var mode string
	var ok bool
	if spec.Name == "sed" {
		mode, ok = SelectSedMode(ctx, bound)
	} else {
		mode, ok = SelectMultiMode(spec, bound)
	}
	if !ok || !KnownMode(spec.Name, mode) {
		return BoundInvocation{}, false
	}
	return BoundInvocation{Command: spec.Name, Mode: mode}, true
}
