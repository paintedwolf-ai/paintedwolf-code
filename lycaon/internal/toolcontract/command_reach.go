package toolcontract

import (
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Reach marks represent command effect axes in a reader's held set.
const (
	ReachCommandMutation             = "command_mutation"
	ReachCommandExternalExec         = "command_external_exec"
	ReachCommandExternalExecSelector = "command_external_exec_selector"
	ReachCommandNetworkEgress        = "command_network_egress"
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

// IsReachMark reports whether name is a synthetic command-effect reach mark.
func IsReachMark(name string) bool {
	switch name {
	case ReachCommandMutation, ReachCommandExternalExec, ReachCommandExternalExecSelector, ReachCommandNetworkEgress:
		return true
	default:
		return false
	}
}

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
		mutates := len(spec.Effects) > 0
		for _, effect := range spec.Effects {
			// A command with at least one non-mutating mode (e.g. find's default
			// print or sed's stream-script) is reachable on read-only surfaces;
			// wrappers whose child effects are unbound on every mode stay guarded.
			required, err := effect.CommandEffects(nil)
			if err == nil && !required.Allows(mutation) {
				mutates = false
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

var backtickedSpan = regexp.MustCompile("`([^`\n]+)`")

// MutatingMixedModeInvocationsIn scans backticked command invocations in corpus
// and returns the canonical command names whose concrete flags or syntax select
// a mutating effect mode (such as `sed -i`, `find ... -delete`, `yq -i`, `sort -o`,
// `iconv -o`, `uniq INPUT OUTPUT`, or archive writes without `-t`/`-l`/`-c`).
func MutatingMixedModeInvocationsIn(corpus string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range backtickedSpan.FindAllStringSubmatch(corpus, -1) {
		span := strings.TrimSpace(m[1])
		for _, seg := range splitBacktickedStatements(span) {
			words := tokenizeBacktickedWords(seg)
			if len(words) == 0 {
				continue
			}
			if cmd := mutatingMixedModeCommand(words); cmd != "" && !seen[cmd] {
				seen[cmd] = true
				out = append(out, cmd)
			}
		}
	}
	slices.Sort(out)
	return out
}

func splitBacktickedStatements(span string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(span); i++ {
		c := span[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			cur.WriteByte(c)
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			cur.WriteByte(c)
			continue
		}
		if c == ';' || c == '|' || c == '&' {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func tokenizeBacktickedWords(seg string) []string {
	var words []string
	var cur strings.Builder
	var quote byte
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case ' ', '\t':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return words
}

func mutatingMixedModeCommand(words []string) string {
	cmd := words[0]
	args := words[1:]
	if len(args) == 0 {
		return ""
	}
	switch cmd {
	case "sed":
		for _, a := range args {
			if a == "-i" || a == "-I" || strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "--in-place") {
				return "sed"
			}
		}
	case "find":
		for i, a := range args {
			if a == "-delete" {
				return "find"
			}
			if a == "-exec" && i+1 < len(args) {
				child := args[i+1:]
				if end := slices.IndexFunc(child, func(w string) bool { return w == ";" || w == "+" }); end > 0 {
					child = child[:end]
				}
				if len(child) > 0 && !CommandLanguageReachable(child[0], map[string]bool{"command": true}) {
					return "find"
				}
				if mutatingMixedModeCommand(child) != "" {
					return "find"
				}
			}
		}
	case "yq":
		for _, a := range args {
			if a == "-i" || a == "--inplace" {
				return "yq"
			}
		}
	case "sort", "iconv":
		for _, a := range args {
			if a == "-o" || strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "--output") {
				return cmd
			}
		}
	case "tar":
		// Listing (-t / -tf / -ztvf) is read-only; create/extract (-c / -x) mutates.
		hasList := false
		hasMutate := false
		for _, a := range args {
			if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
				if strings.ContainsRune(a, 't') {
					hasList = true
				}
				if strings.ContainsAny(a, "cxru") {
					hasMutate = true
				}
			}
		}
		if hasMutate && !hasList {
			return "tar"
		}
	case "zip":
		if !slices.Contains(args, "-l") {
			return "zip"
		}
	case "unzip":
		if !slices.Contains(args, "-l") && !slices.Contains(args, "-p") && !slices.Contains(args, "-c") {
			return "unzip"
		}
	}
	return ""
}
