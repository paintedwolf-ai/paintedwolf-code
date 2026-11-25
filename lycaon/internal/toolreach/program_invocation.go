package toolreach

import (
	"context"
	"maps"
	"sync"

	"github.com/lycaon/lycaon/internal/commandmode"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ProgramTable holds frozen program and effect-mode admission for one request.
type ProgramTable struct {
	programs Programs
	modes    ProgramModes
}

// NewProgramTable builds a frozen program and effect-mode table from admitted maps.
func NewProgramTable(programs map[string]bool, modes map[string]map[string]bool) *ProgramTable {
	progCopy := maps.Clone(programs)
	modeCopy := make(map[string]map[string]bool, len(modes))
	for k, v := range modes {
		modeCopy[k] = maps.Clone(v)
	}
	return &ProgramTable{
		programs: func(name string) bool { return progCopy[name] },
		modes:    func(name, mode string) bool { return modeCopy[name][mode] },
	}
}

// TableFromPrograms wraps a program predicate without assuming mutating modes.
func TableFromPrograms(programs Programs) *ProgramTable {
	if programs == nil {
		return nil
	}
	return &ProgramTable{programs: programs}
}

// Program reports whether command name is admitted in the table.
func (t *ProgramTable) Program(name string) bool {
	return t != nil && t.programs != nil && t.programs(name)
}

// Mode reports whether command name and effect mode are admitted in the table.
func (t *ProgramTable) Mode(name, mode string) bool {
	if t == nil || !commandmode.KnownMode(name, mode) {
		return false
	}
	if t.modes != nil {
		return t.modes(name, mode)
	}
	return t.programs != nil && t.programs(name) && loadGuaranteedModes()[name][mode]
}

var loadGuaranteedModes = sync.OnceValue(func() map[string]map[string]bool {
	guaranteed := map[string]map[string]bool{}
	catalog, err := toolcontract.ReadOnlyLanguageV1()
	if err != nil {
		return guaranteed
	}
	readOnly, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	allEffects := toolcontract.AllCommandEffects()
	for _, spec := range catalog.Commands {
		modes := make(map[string]bool, len(spec.Effects))
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
		guaranteed[spec.Name] = modes
	}
	return guaranteed
})

// ProgramMode reports whether command name and effect mode are admitted on this request.
func (r Request) ProgramMode(name, mode string) bool {
	if r.Status(ProgramTool) == Absent || !commandmode.KnownMode(name, mode) {
		return false
	}
	if r.programModes != nil {
		return r.programModes(name, mode)
	}
	if r.programs == nil || !r.programs(name) {
		return false
	}
	return loadGuaranteedModes()[name][mode]
}

// ProgramInvocation reports whether concrete command words are admitted on this request.
func (r Request) ProgramInvocation(words ...string) bool {
	if r.Status(ProgramTool) == Absent || len(words) == 0 {
		return false
	}
	bound, ok := commandmode.Resolve(context.Background(), words)
	if !ok {
		return false
	}
	return r.ProgramMode(bound.Command, bound.Mode)
}

func probeWords(call Call) []string {
	if len(call.probe) > 0 {
		return call.probe
	}
	return call.words
}
