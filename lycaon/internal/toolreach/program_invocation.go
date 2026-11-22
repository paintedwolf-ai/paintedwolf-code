package toolreach

import (
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type catalogModeIndex struct {
	specs           map[string]toolcontract.CommandSpecification
	twoWordByFamily map[string][]toolcontract.CommandSpecification
	guaranteedModes map[string]map[string]bool
}

var loadCatalogModeIndex = sync.OnceValue(func() catalogModeIndex {
	idx := catalogModeIndex{
		specs:           map[string]toolcontract.CommandSpecification{},
		twoWordByFamily: map[string][]toolcontract.CommandSpecification{},
		guaranteedModes: map[string]map[string]bool{},
	}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return idx
	}
	readOnly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	allEffects, _ := toolcontract.NewCommandEffects(
		toolcontract.CommandReadOnly,
		toolcontract.CommandMutation,
		toolcontract.CommandExternalExec,
		toolcontract.CommandNetworkEgress,
	)
	for _, spec := range catalog.Commands {
		idx.specs[spec.Name] = spec
		if parts := strings.Fields(spec.Name); len(parts) > 1 {
			idx.twoWordByFamily[parts[0]] = append(idx.twoWordByFamily[parts[0]], spec)
		}
		modes := map[string]bool{}
		if len(spec.Effects) == 1 {
			modes[spec.Effects[0].Mode] = true
		} else {
			for _, target := range spec.Effects {
				reqEffects, err := target.CommandEffects(&allEffects)
				if err != nil {
					continue
				}
				subsetOfAll := true
				for _, other := range spec.Effects {
					otherEffects, err := other.CommandEffects(&readOnly)
					if err != nil || !(otherEffects | readOnly).Allows(reqEffects) {
						subsetOfAll = false
						break
					}
				}
				modes[target.Mode] = subsetOfAll
			}
		}
		idx.guaranteedModes[spec.Name] = modes
	}
	return idx
})

// ProgramModeKey encodes a (command, mode) query inside a Programs lookup so
// production ProgramReach closures carry exact mode-level admission without
// requiring callers to thread a separate field through turn contexts.
func ProgramModeKey(name, mode string) string {
	return name + "\x00" + mode
}

// ProgramMode reports whether a specific catalog command and effect mode are
// admitted on this frozen request. Without explicit mode reach, multi-mode
// commands admit only modes whose effects are implied by every mode of that
// command (never mutating/extracting modes solely from read reach).
func (r Request) ProgramMode(name, mode string) bool {
	if r.Status(ProgramTool) == Absent {
		return false
	}
	if r.programModes != nil {
		return r.programModes(name, mode)
	}
	if r.programs == nil {
		return false
	}
	if r.programs(ProgramModeKey(name, "")) {
		return r.programs(ProgramModeKey(name, mode))
	}
	if !r.programs(name) {
		return false
	}
	return loadCatalogModeIndex().guaranteedModes[name][mode]
}

// ProgramInvocation binds a concrete argv invocation through the production
// command-language catalog binder (resolving two-word subcommands such as
// "git config" before generic "git") and checks admission of the bound
// operation and effect mode.
func (r Request) ProgramInvocation(words ...string) bool {
	if r.Status(ProgramTool) == Absent || len(words) == 0 {
		return false
	}
	spec, mode, ok := bindCatalogInvocation(words)
	if !ok {
		return false
	}
	return r.ProgramMode(spec.Name, mode)
}

func bindCatalogInvocation(words []string) (toolcontract.CommandSpecification, string, bool) {
	if len(words) == 0 || strings.TrimSpace(words[0]) == "" {
		return toolcontract.CommandSpecification{}, "", false
	}
	idx := loadCatalogModeIndex()
	fields := make(commandprogram.Fields, len(words))
	for i, w := range words {
		fields[i] = commandprogram.Field{Value: w}
	}
	binder := commandprogram.CatalogBinder{}
	if candidates := idx.twoWordByFamily[words[0]]; len(candidates) > 0 && len(words) > 1 {
		if direct, ok := idx.specs[words[0]+" "+words[1]]; ok {
			if bound, err := binder.Bind(direct, fields); err == nil {
				mode, valid := selectBoundInvocationMode(direct, bound)
				return direct, mode, valid
			}
		}
		for _, candidate := range candidates {
			if bound, err := binder.Bind(candidate, fields); err == nil {
				mode, valid := selectBoundInvocationMode(candidate, bound)
				return candidate, mode, valid
			}
		}
		if words[0] == "git" {
			return toolcontract.CommandSpecification{}, "", false
		}
	}
	spec, ok := idx.specs[words[0]]
	if !ok {
		return toolcontract.CommandSpecification{}, "", false
	}
	bound, err := binder.Bind(spec, fields)
	if err != nil {
		return toolcontract.CommandSpecification{}, "", false
	}
	mode, valid := selectBoundInvocationMode(spec, bound)
	return spec, mode, valid
}

func selectBoundInvocationMode(spec toolcontract.CommandSpecification, bound commandprogram.BoundArgv) (string, bool) {
	if len(spec.Effects) == 0 {
		return "", false
	}
	if len(spec.Effects) == 1 {
		return spec.Effects[0].Mode, true
	}
	hasOpt := func(key string) bool {
		_, ok := bound.Last(key)
		return ok
	}
	switch spec.Name {
	case "tar":
		if hasOpt("-t") {
			return "listing-or-stream-decode", true
		}
		return "create-extract-compress-decompress-to-path", true
	case "zip", "unzip":
		if hasOpt("-l") || (spec.Name == "unzip" && hasOpt("-p")) {
			return "listing-or-stream-decode", true
		}
		return "create-extract-compress-decompress-to-path", true
	case "gzip":
		if hasOpt("-t") || hasOpt("-c") {
			return "listing-or-stream-decode", true
		}
		return "create-extract-compress-decompress-to-path", true
	case "gunzip":
		if hasOpt("-c") || len(bound.Operands()) == 0 {
			return "listing-or-stream-decode", true
		}
		return "create-extract-compress-decompress-to-path", true
	case "sed":
		if hasOpt("-i") {
			return "-i", true
		}
		if hasOpt("-f") {
			return "", false
		}
		return "stream-script", true
	case "find":
		if hasOpt("-delete") {
			return "-delete", true
		}
		if hasOpt("-exec") {
			return "-exec", true
		}
		return "default-predicates-print", true
	case "yq":
		if hasOpt("-i") {
			return "-i", true
		}
		return "query", true
	case "sort", "iconv":
		if hasOpt("-o") {
			return "-o", true
		}
		return "always", true
	case "uniq":
		if len(bound.Operands()) > 1 {
			return "OUTPUT-supplied", true
		}
		return "stdin-input-only", true
	case "code-rewrite", "patch":
		if hasOpt("--dry-run") {
			return "dry-run", true
		}
		return "mutation", true
	case "curl":
		return selectCurlInvocationMode(bound), true
	default:
		return spec.Effects[0].Mode, true
	}
}

func selectCurlInvocationMode(bound commandprogram.BoundArgv) string {
	_, managedCookies := bound.Last("--managed-cookie-jar")
	captures := len(bound.Options("--capture-token")) > 0
	updatesJar := managedCookies || captures
	for _, key := range []string{"-o", "-D", "-c"} {
		if opt, ok := bound.Last(key); ok && len(opt.Values) > 0 {
			dest := opt.Values[0].Value
			if dest != "" && dest != "-" && dest != "/dev/null" {
				if updatesJar {
					return "managed-jar-update-to-file"
				}
				return "-o--D--c-to-file"
			}
		}
	}
	if updatesJar {
		return "managed-jar-update"
	}
	return "request"
}
