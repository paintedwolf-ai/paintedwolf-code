package toolcontract

import (
	"regexp"
	"sync"
)

// ReachCommandMutation marks a reader able to run command programs that mutate
// the workspace.
const ReachCommandMutation = "command_mutation"

// namedForm matches backticked hyphenated command forms, which plain identifier
// scanners cannot see because their character class omits hyphens.
var namedForm = regexp.MustCompile("`([a-z][a-z0-9_]*-[a-z0-9_-]*)(?:`|[ \t]*\\(|[ \t]+--)")

type commandReachTables struct {
	commands map[string]bool
	mutating map[string]bool
	formTool map[string]string
	forms    map[string]bool
	retired  map[string][]string
	// aliasForm maps a declared command alias to its canonical command name.
	aliasForm map[string]string
	// commandAliases maps a canonical command name to its declared aliases.
	commandAliases map[string][]string
}

var loadCommandReachTables = sync.OnceValue(func() *commandReachTables {
	language, err := ReadOnlyLanguageV1()
	if err != nil {
		return nil
	}
	mutation, err := NewCommandEffects(CommandMutation)
	if err != nil {
		return nil
	}
	out := &commandReachTables{
		commands:       map[string]bool{},
		mutating:       map[string]bool{},
		formTool:       map[string]string{},
		forms:          map[string]bool{},
		retired:        map[string][]string{},
		aliasForm:      map[string]string{},
		commandAliases: map[string][]string{},
	}
	for _, spec := range language.Commands {
		out.commands[spec.Name] = true
		if len(spec.Aliases) > 0 {
			out.commandAliases[spec.Name] = spec.Aliases
			for _, alias := range spec.Aliases {
				out.aliasForm[alias] = spec.Name
			}
		}
		mutates := false
		for _, effect := range spec.Effects {
			// A wrapper whose child effects are unbound cannot promise
			// read-only; treat it as mutating so copy stays guarded.
			required, err := effect.CommandEffects(nil)
			if err != nil || required.Allows(mutation) {
				mutates = true
			}
		}
		if mutates {
			out.mutating[spec.Name] = true
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

// admitForm admits a command form and its declared aliases as hyphenated
// command forms the scanner can see; the parser admits both spellings.
func (t *commandReachTables) admitForm(form string) {
	t.forms[form] = true
	for _, alias := range t.commandAliases[form] {
		t.forms[alias] = true
	}
}

// CommandLanguageReachable reports whether a reader with held can run name as a
// command program: under command, through a form the language admits, honoring
// each form's mutation requirement and dual-state tool gate. A retired
// operation is reached through the forms that replaced it, and a declared
// command alias reaches through its canonical form.
func CommandLanguageReachable(name string, held map[string]bool) bool {
	lang := loadCommandReachTables()
	if lang == nil {
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
	if !held["command"] {
		return false
	}
	for _, form := range forms {
		if tool := lang.formTool[form]; tool != "" && !held[tool] {
			continue
		}
		if lang.mutating[form] && !held[ReachCommandMutation] {
			continue
		}
		return true
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
