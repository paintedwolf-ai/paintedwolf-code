package toolcontract

import (
	"regexp"
	"sync"
)

// Reach marks represent command effect axes and profile operation exclusions in a reader's held set.
const (
	ReachCommandMutation             = "command_mutation"
	ReachCommandExternalExec         = "command_external_exec"
	ReachCommandExternalExecSelector = "command_external_exec_selector"
	ReachCommandNetworkEgress        = "command_network_egress"
	ReachCommandDeniedPrefix         = "command_denied:"
)

// ReachMarks returns the synthetic reach marks for a reader's command effects.
func ReachMarks(holdsCommand bool, effects CommandEffects) map[string]bool {
	out := make(map[string]bool)
	if !holdsCommand {
		return out
	}
	if effects.Allows(commandEffectBit(CommandMutation)) {
		out[ReachCommandMutation] = true
	}
	if effects.Allows(commandEffectBit(CommandExternalExec)) {
		out[ReachCommandExternalExec] = true
		out[ReachCommandExternalExecSelector] = true
	}
	if effects.Allows(commandEffectBit(CommandNetworkEgress)) {
		out[ReachCommandNetworkEgress] = true
	}
	return out
}

// DeniedCommandMarks returns synthetic exclusion marks for native command forms
// that held and effects would otherwise admit when isDenied excludes their policy identity.
func DeniedCommandMarks(held map[string]bool, effects CommandEffects, isDenied func(string) bool) map[string]bool {
	out := make(map[string]bool)
	if !held["command"] || isDenied == nil {
		return out
	}
	lang := loadCommandReachTables()
	if lang == nil {
		return out
	}
	for name, spec := range lang.specs {
		if tool := lang.formTool[name]; tool != "" && !held[tool] {
			continue
		}
		if !specAllowsEffects(spec, effects) {
			continue
		}
		for _, candidate := range lang.identities[name] {
			if isDenied(candidate) {
				out[ReachCommandDeniedPrefix+candidate] = true
			}
		}
	}
	return out
}

// namedForm matches backticked hyphenated command forms, which plain identifier
// scanners cannot see because their character class omits hyphens.
var namedForm = regexp.MustCompile("`([a-z][a-z0-9_]*-[a-z0-9_-]*)(?:`|[ \t]*\\(|[ \t]+--)")

type commandReachTables struct {
	commands       map[string]bool
	specs          map[string]CommandSpecification
	identities     map[string][]string
	formTool       map[string]string
	forms          map[string]bool
	retired        map[string][]string
	aliasForm      map[string]string
	commandAliases map[string][]string
}

var loadCommandReachTables = sync.OnceValue(func() *commandReachTables {
	language, err := ReadOnlyLanguageV1()
	if err != nil {
		return nil
	}
	identities, err := policyIdentitiesByCommand()
	if err != nil {
		return nil
	}
	out := &commandReachTables{
		commands:       map[string]bool{},
		specs:          map[string]CommandSpecification{},
		identities:     map[string][]string{},
		formTool:       map[string]string{},
		forms:          map[string]bool{},
		retired:        map[string][]string{},
		aliasForm:      map[string]string{},
		commandAliases: map[string][]string{},
	}
	for _, spec := range language.Commands {
		out.commands[spec.Name] = true
		out.specs[spec.Name] = spec
		if ids, ok := identities[spec.Name]; ok {
			out.identities[spec.Name] = ids
		} else {
			out.identities[spec.Name] = []string{spec.Name}
		}
		if len(spec.Aliases) > 0 {
			out.commandAliases[spec.Name] = spec.Aliases
			for _, alias := range spec.Aliases {
				out.aliasForm[alias] = spec.Name
			}
		}
	}
	for _, disposition := range language.Tools {
		switch disposition.Disposition {
		case DispositionToolAndCommandForm:
			for _, form := range disposition.CommandForms {
				out.formTool[form] = disposition.Tool
				out.admitForm(form)
			}
		case RoutineSchemaRetired:
			out.retired[disposition.Tool] = disposition.CommandForms
			for _, form := range disposition.CommandForms {
				out.admitForm(form)
			}
		}
	}
	return out
})

func (t *commandReachTables) admitForm(form string) {
	t.forms[form] = true
	for _, alias := range t.commandAliases[form] {
		t.forms[alias] = true
	}
}

func heldCommandEffects(held map[string]bool) CommandEffects {
	if !held["command"] {
		return 0
	}
	effects := commandEffectBit(CommandReadOnly)
	if held[ReachCommandMutation] {
		effects |= commandEffectBit(CommandMutation)
	}
	if held[ReachCommandExternalExec] {
		effects |= commandEffectBit(CommandExternalExec)
	}
	if held[ReachCommandNetworkEgress] {
		effects |= commandEffectBit(CommandNetworkEgress)
	}
	return effects
}

func specAllowsEffects(spec CommandSpecification, allowed CommandEffects) bool {
	if allowed == 0 {
		return false
	}
	for _, requirement := range spec.Effects {
		required, err := requirement.CommandEffects(&allowed)
		if err == nil && allowed.Allows(required) {
			return true
		}
	}
	return false
}

// CommandLanguageReachable reports whether a reader with held can run name as a
// command program: under command, through an admitted form whose effect modes,
// dual-state form tool gate, external selector visibility, and profile operation
// exclusions are all satisfied.
func CommandLanguageReachable(name string, held map[string]bool) bool {
	lang := loadCommandReachTables()
	if lang == nil || !held["command"] {
		return false
	}
	forms, ok := lang.retired[name]
	if !ok {
		switch {
		case lang.commands[name]:
			forms = []string{name}
		case lang.formTool[name] != "":
			forms = []string{name}
		case lang.aliasForm[name] != "":
			forms = []string{lang.aliasForm[name]}
		default:
			return false
		}
	}
	allowed := heldCommandEffects(held)
	for _, form := range forms {
		if tool := lang.formTool[form]; tool != "" && !held[tool] {
			continue
		}
		if form == "external" && !held[ReachCommandExternalExecSelector] {
			continue
		}
		if lang.formDenied(form, held) {
			continue
		}
		if spec, found := lang.specs[form]; found && specAllowsEffects(spec, allowed) {
			return true
		}
	}
	return false
}

func (t *commandReachTables) formDenied(form string, held map[string]bool) bool {
	for _, candidate := range t.identities[form] {
		if held[ReachCommandDeniedPrefix+candidate] {
			return true
		}
	}
	return false
}

// CommandFormToolOf returns the dual-state tool that gates a command form, or
// empty when no dual-state tool gates it. A declared command alias reports the
// gate of its canonical form.
func CommandFormToolOf(name string) string {
	lang := loadCommandReachTables()
	if lang == nil {
		return ""
	}
	if tool := lang.formTool[name]; tool != "" {
		return tool
	}
	return lang.formTool[lang.aliasForm[name]]
}

// HyphenatedCommandFormsIn returns the declared hyphenated command forms,
// including declared aliases of those forms, named as backticked invocations in
// corpus.
func HyphenatedCommandFormsIn(corpus string) []string {
	lang := loadCommandReachTables()
	if lang == nil {
		return nil
	}
	var out []string
	for _, m := range namedForm.FindAllStringSubmatch(corpus, -1) {
		name := m[1]
		if lang.forms[name] {
			out = append(out, name)
		}
	}
	return out
}
