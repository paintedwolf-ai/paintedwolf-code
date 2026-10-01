// Command codegen-tool-command-equivalence synchronizes command grammars.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"

	"gopkg.in/yaml.v3"
)

// maxEquivalenceLineRunes bounds one generated equivalence line.
const maxEquivalenceLineRunes = 120

type equivalenceFile struct {
	Entries []equivalenceEntry `yaml:"entries"`
}

type equivalenceEntry struct {
	Native             string               `yaml:"native"`
	Replaces           []replaceHabit       `yaml:"replaces"`
	ReplacementExample string               `yaml:"replacement_example"`
	ArgsHint           string               `yaml:"args_hint"`
	CommandGrammar     []commandGrammarProg `yaml:"command_grammar"`
	RedirectCode       string               `yaml:"redirect_code"`
	Profiles           []string             `yaml:"profiles"`
	ReadHabit          []readHabitProg      `yaml:"read_habit"`
}

// readHabitProg names one program whose argv confine can translate into a read
// call, and the argument grammar that parses it.
type readHabitProg struct {
	Program string `yaml:"program"`
	Grammar string `yaml:"grammar"`
}

type replaceHabit struct {
	Habit    string   `yaml:"habit"`
	Examples []string `yaml:"examples"`
}

type commandGrammarProg struct {
	Program string `yaml:"program"`
	Grammar string `yaml:"grammar"`
}

func main() {
	root := flag.String("root", ".", "lycaon module root (contains config/)")
	check := flag.Bool("check", false, "exit 1 if generated outputs are stale")
	flag.Parse()

	paths, err := resolveModulePaths(*root)
	if err != nil {
		fatal(err)
	}

	eq, err := loadEquivalence(paths.equivalenceYAML)
	if err != nil {
		fatal(err)
	}

	genSrc, err := renderGenGo(eq)
	if err != nil {
		fatal(err)
	}
	if err := writeOrCheck(paths.generatedGo, genSrc, *check); err != nil {
		fatal(err)
	}
	habitSrc, err := renderReadHabitGo(eq)
	if err != nil {
		fatal(err)
	}
	if err := writeOrCheck(paths.generatedReadHabitGo, habitSrc, *check); err != nil {
		fatal(err)
	}
	if err := patchToolSchemas(paths.toolSchemasDir, eq, *check); err != nil {
		fatal(err)
	}
}

func loadEquivalence(path string) (*equivalenceFile, error) {
	data, err := readTrackedFile(path)
	if err != nil {
		return nil, err
	}
	var eq equivalenceFile
	if err := yaml.Unmarshal(data, &eq); err != nil {
		return nil, err
	}
	if len(eq.Entries) == 0 {
		return nil, fmt.Errorf("%s: no entries", path)
	}
	return &eq, nil
}

func writeOrCheck(path string, content []byte, check bool) error {
	if check {
		existing, err := readTrackedFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w (run ./task codegen:tool-command-equivalence)", path, err)
		}
		if !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(content)) {
			return fmt.Errorf("%s is stale — run ./task codegen:tool-command-equivalence", path)
		}
		return nil
	}
	return writeTrackedFile(path, content)
}

func replacesSuffix(entry equivalenceEntry) string {
	if len(entry.Replaces) == 0 {
		return fmt.Sprintf(" Replaces command: use native `%s`.", entry.Native)
	}
	habits := make([]string, 0, len(entry.Replaces))
	for _, r := range entry.Replaces {
		h := strings.TrimSpace(r.Habit)
		if h != "" {
			habits = append(habits, h)
		}
	}
	line := " Replaces command: " + strings.Join(habits, ", ") + "."
	line = runeclamp.Fit(line, maxEquivalenceLineRunes)
	return line
}

func patchToolSchemas(dir string, eq *equivalenceFile, check bool) error {
	suffixes := map[string]string{}
	for _, entry := range eq.Entries {
		suffixes[entry.Native] = replacesSuffix(entry)
	}
	for native, suffix := range suffixes {
		path := filepath.Join(dir, native+".yaml")
		data, err := readTrackedFile(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		patched, err := patchUnitSchemaDescription(string(data), suffix)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if check {
			if string(data) != patched {
				return fmt.Errorf("%s is stale — run ./task codegen:tool-command-equivalence", path)
			}
			continue
		}
		if err := writeTrackedFile(path, []byte(patched)); err != nil {
			return err
		}
	}
	return nil
}

func patchUnitSchemaDescription(text, suffix string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return "", fmt.Errorf("parse tool schema: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("tool schema must be a mapping")
	}
	fields := doc.Content[0].Content
	lines := strings.Split(text, "\n")
	for i := 0; i < len(fields); i += 2 {
		key, value := fields[i], fields[i+1]
		if key.Value != "description" {
			continue
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return "", fmt.Errorf("description must be a string")
		}
		description := stripReplacesSuffix(value.Value) + suffix
		if description == value.Value {
			return text, nil
		}
		end := len(lines)
		if i+2 < len(fields) {
			end = fields[i+2].Line - 1
		}
		for end > key.Line && (strings.TrimSpace(lines[end-1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[end-1]), "#")) {
			end--
		}
		updated := append([]string{}, lines[:key.Line-1]...)
		updated = append(updated, "description: "+strconv.Quote(description))
		updated = append(updated, lines[end:]...)
		return strings.Join(updated, "\n"), nil
	}
	return "", fmt.Errorf("description is required")
}

func stripReplacesSuffix(desc string) string {
	if i := strings.Index(desc, " Replaces command:"); i >= 0 {
		return strings.TrimSpace(desc[:i])
	}
	return strings.TrimSpace(desc)
}

func renderGenGo(eq *equivalenceFile) ([]byte, error) {
	entries := append([]equivalenceEntry(nil), eq.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Native < entries[j].Native
	})

	var b strings.Builder
	fmt.Fprintln(&b, "// Code generated by cmd/codegen-tool-command-equivalence. DO NOT EDIT.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "package tools")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "type commandEquivalenceEntry struct {")
	fmt.Fprintln(&b, "\tNative       string")
	fmt.Fprintln(&b, "\tRedirectCode string")
	fmt.Fprintln(&b, "\tArgsHint     string")
	fmt.Fprintln(&b, "\tExample      string")
	fmt.Fprintln(&b, "\tReplacesLine string")
	fmt.Fprintln(&b, "\tProfiles     map[string]bool")
	fmt.Fprintln(&b, "}")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "var commandEquivalenceEntries = []commandEquivalenceEntry{")

	for _, entry := range entries {
		example := strings.TrimSpace(entry.ReplacementExample)
		if example == "" && len(entry.Replaces) > 0 && len(entry.Replaces[0].Examples) > 0 {
			example = entry.Replaces[0].Examples[0]
		}
		fmt.Fprintf(&b, "\t{\n")
		fmt.Fprintf(&b, "\t\tNative:       %q,\n", entry.Native)
		fmt.Fprintf(&b, "\t\tRedirectCode: %q,\n", entry.RedirectCode)
		fmt.Fprintf(&b, "\t\tArgsHint:     %q,\n", entry.ArgsHint)
		fmt.Fprintf(&b, "\t\tExample:      %q,\n", example)
		fmt.Fprintf(&b, "\t\tReplacesLine: %q,\n", replacesSuffix(entry))
		fmt.Fprintln(&b, "\t\tProfiles: map[string]bool{")
		for _, p := range entry.Profiles {
			fmt.Fprintf(&b, "\t\t\t%q: true,\n", p)
		}
		fmt.Fprintln(&b, "\t\t},")
		fmt.Fprintln(&b, "\t},")
	}
	fmt.Fprintln(&b, "}")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "// commandGrammarFor selects a closed exact-translation grammar by program name.")
	fmt.Fprintln(&b, "var commandGrammarFor = map[string]string{")
	grammars, err := commandGrammarRows(entries)
	if err != nil {
		return nil, err
	}
	for _, row := range grammars {
		fmt.Fprintf(&b, "\t%q: %q,\n", row.program, row.grammar)
	}
	fmt.Fprintln(&b, "}")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "var commandSurveyNativeTools = []string{")
	seen := map[string]bool{}
	for _, entry := range eq.Entries {
		if entry.Native == "chmod" || entry.Native == "delete" {
			continue
		}
		if !seen[entry.Native] {
			seen[entry.Native] = true
			fmt.Fprintf(&b, "\t%q,\n", entry.Native)
		}
	}
	fmt.Fprintln(&b, "}")
	// Format generated Go before comparison or write.
	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated source: %w", err)
	}
	return formatted, nil
}

type commandGrammarRow struct{ program, grammar string }

var commandGrammars = map[string]struct{}{
	"sleep": {}, "curl": {}, "list_dir": {}, "stat": {}, "wc": {},
	"delete": {}, "copy": {}, "move": {}, "mkdir": {}, "chmod": {},
	"chown": {}, "diff": {}, "extract_zip": {}, "jq_json": {},
	"jq_yaml": {}, "grep": {}, "find": {}, "tree": {}, "git": {},
}

func commandGrammarRows(entries []equivalenceEntry) ([]commandGrammarRow, error) {
	var rows []commandGrammarRow
	seen := map[string]string{}
	for _, entry := range entries {
		for _, declared := range entry.CommandGrammar {
			program := strings.TrimSpace(declared.Program)
			grammar := strings.TrimSpace(declared.Grammar)
			if program == "" || grammar == "" {
				return nil, fmt.Errorf("command_grammar under %q needs both program and grammar", entry.Native)
			}
			if _, ok := commandGrammars[grammar]; !ok {
				return nil, fmt.Errorf("command program %q names unknown grammar %q", program, grammar)
			}
			if prior, duplicate := seen[program]; duplicate {
				return nil, fmt.Errorf("command program %q declared twice (%s, %s)", program, prior, grammar)
			}
			seen[program] = grammar
			rows = append(rows, commandGrammarRow{program: program, grammar: grammar})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].program < rows[j].program })
	return rows, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// readHabitGrammars lists supported confinement grammars.
var readHabitGrammars = map[string]string{
	"whole_file":      "readHabitWholeFile",
	"leading_lines":   "readHabitLeadingLines",
	"quiet_line_span": "readHabitQuietLineSpan",
}

// renderReadHabitGo emits the confinement grammar table.
func renderReadHabitGo(eq *equivalenceFile) ([]byte, error) {
	type row struct{ program, grammarConst string }
	var rows []row
	seen := map[string]string{}
	for _, entry := range eq.Entries {
		for _, h := range entry.ReadHabit {
			program := strings.TrimSpace(h.Program)
			grammar := strings.TrimSpace(h.Grammar)
			if program == "" || grammar == "" {
				return nil, fmt.Errorf("read_habit under %q needs both program and grammar", entry.Native)
			}
			constName, ok := readHabitGrammars[grammar]
			if !ok {
				return nil, fmt.Errorf("read_habit %q names unknown grammar %q", program, grammar)
			}
			if prior, dup := seen[program]; dup {
				return nil, fmt.Errorf("read_habit program %q declared twice (%s, %s)", program, prior, grammar)
			}
			seen[program] = grammar
			rows = append(rows, row{program: program, grammarConst: constName})
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no read_habit programs declared")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].program < rows[j].program })

	var b strings.Builder
	fmt.Fprintln(&b, "// Code generated by cmd/codegen-tool-command-equivalence. DO NOT EDIT.")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "package confine")
	fmt.Fprintln(&b, "")
	fmt.Fprintln(&b, "// readHabitGrammarFor maps a program name to the argument grammar that")
	fmt.Fprintln(&b, "// translates it into a native read.")
	fmt.Fprintln(&b, "var readHabitGrammarFor = map[string]readHabitGrammar{")
	for _, r := range rows {
		fmt.Fprintf(&b, "\t%q: %s,\n", r.program, r.grammarConst)
	}
	fmt.Fprintln(&b, "}")
	return format.Source([]byte(b.String()))
}
