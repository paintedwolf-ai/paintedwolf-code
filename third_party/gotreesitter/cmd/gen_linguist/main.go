// Command gen_linguist generates grammars/linguist_gen.go by matching
// gotreesitter grammar names to GitHub Linguist's languages.yml.
//
// Usage:
//
//	go run ./cmd/gen_linguist -manifest grammars/languages.manifest [-languages-yml path] [-out grammars/linguist_gen.go]
//
// If -languages-yml is not specified, fetches from GitHub.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const linguistURL = "https://raw.githubusercontent.com/github-linguist/linguist/master/lib/linguist/languages.yml"

// linguistEntry is the subset of languages.yml we care about.
type linguistEntry struct {
	Type         string   `yaml:"type"`
	Aliases      []string `yaml:"aliases"`
	Extensions   []string `yaml:"extensions"`
	Filenames    []string `yaml:"filenames"`
	Interpreters []string `yaml:"interpreters"`
}

// grammarToLinguist maps gotreesitter names that can't be auto-matched
// to their linguist canonical name. Empty string means explicitly no match.
var grammarToLinguist = map[string]string{
	"bash":              "Shell",
	"c_sharp":           "C#",
	"commonlisp":        "Common Lisp",
	"cpp":               "C++",
	"cuda":              "Cuda",
	"dockerfile":        "Dockerfile",
	"elisp":             "Emacs Lisp",
	"embedded_template": "HTML+ERB",
	"fsharp":            "F#",
	"gdscript":          "GDScript",
	"gitattributes":     "Gitattributes",
	"gitcommit":         "Git Commit",
	"git_config":        "Git Config",
	"gitignore":         "Gitignore",
	"git_rebase":        "Git Rebase",
	"godot_resource":    "Godot Resource",
	"gomod":             "Go Module",
	"hack":              "Hack",
	"heex":              "HTML+EEX",
	"javascript":        "JavaScript",
	"jsdoc":             "JSDoc",
	"json5":             "JSON5",
	"jsonnet":           "Jsonnet",
	"linkerscript":      "Linker Script",
	"make":              "Makefile",
	"markdown_inline":   "",
	"matlab":            "MATLAB",
	"nushell":           "Nu",
	"objc":              "Objective-C",
	"pascal":            "Pascal",
	"powershell":        "PowerShell",
	"proto":             "Protocol Buffer",
	"ql":                "CodeQL",
	"regex":             "",
	"comment":           "",
	"requirements":      "Pip Requirements",
	"rescript":          "ReScript",
	"scss":              "SCSS",
	"ssh_config":        "",
	"starlark":          "Starlark",
	"tablegen":          "TableGen",
	"textproto":         "Protocol Buffer Text Format",
	"tlaplus":           "TLA",
	"tsx":               "TSX",
	"typescript":        "TypeScript",
	"vimdoc":            "Vim Help File",
	"wolfram":           "Wolfram Language",
	// Matched but with non-obvious linguist names.
	"capnp":      "Cap'n Proto",
	"dot":        "Graphviz (DOT)",
	"jinja2":     "Jinja",
	"properties": "Java Properties",
	"rego":       "Open Policy Agent",
	"robot":      "RobotFramework",
	"wat":        "WebAssembly",
	// No linguist entry — suppress warnings.
	"angular":     "",
	"arduino":     "",
	"authzed":     "",
	"bass":        "",
	"beancount":   "",
	"chatito":     "",
	"corn":        "",
	"cpon":        "",
	"devicetree":  "",
	"disassembly": "",
	"djot":        "",
	"doxygen":     "",
	"dtd":         "",
	"eds":         "",
	"elsa":        "",
	"enforce":     "",
	"facility":    "",
	"fidl":        "",
	"foam":        "",
	"hyprlang":    "",
	"kconfig":     "",
	"ledger":      "",
	"norg":        "",
	"pem":         "",
	"promql":      "",
	"tmux":        "",
	"todotxt":     "",
	"uxntal":      "",
	"yuck":        "",
}

func main() {
	manifestPath := flag.String("manifest", "grammars/languages.manifest", "path to languages.manifest")
	langYMLPath := flag.String("languages-yml", "", "local path to languages.yml (fetches from GitHub if empty)")
	outPath := flag.String("out", "grammars/linguist_gen.go", "output Go file path")
	flag.Parse()

	// 1. Parse manifest for grammar names.
	grammarNames, err := parseManifestNames(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifest: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "manifest: %d grammars\n", len(grammarNames))

	// 2. Load languages.yml.
	langData, err := loadLanguagesYML(*langYMLPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "languages.yml: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "linguist: %d languages\n", len(langData))

	// 3. Build lookup index: normalized linguist name -> canonical name.
	linguistByNormalized := make(map[string]string)
	linguistByAlias := make(map[string]string)
	for canonical, entry := range langData {
		linguistByNormalized[strings.ToLower(canonical)] = canonical
		for _, alias := range entry.Aliases {
			linguistByAlias[strings.ToLower(alias)] = canonical
		}
	}

	// 4. Match each grammar to its linguist entry.
	type match struct {
		grammar      string
		linguistName string
	}
	var matches []match
	var unmatched []string

	for _, g := range grammarNames {
		var lingName string

		// Check explicit override first.
		if override, ok := grammarToLinguist[g]; ok {
			if override == "" {
				matches = append(matches, match{grammar: g})
				continue
			}
			lingName = override
		}

		if lingName == "" {
			if canonical, ok := linguistByNormalized[g]; ok {
				lingName = canonical
			}
		}
		if lingName == "" {
			if canonical, ok := linguistByAlias[g]; ok {
				lingName = canonical
			}
		}
		if lingName == "" {
			normalized := strings.ReplaceAll(g, "_", " ")
			if canonical, ok := linguistByNormalized[normalized]; ok {
				lingName = canonical
			}
		}

		if lingName == "" {
			unmatched = append(unmatched, g)
			matches = append(matches, match{grammar: g})
		} else {
			matches = append(matches, match{grammar: g, linguistName: lingName})
		}
	}

	if len(unmatched) > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: %d grammars unmatched: %s\n", len(unmatched), strings.Join(unmatched, ", "))
	}

	// 5. Build output maps.
	aliasMap := make(map[string]string)
	displayMap := make(map[string]string)
	filenameMap := make(map[string]string)    // exact filename → grammar
	extensionMap := make(map[string]string)   // extension → grammar
	interpreterMap := make(map[string]string) // interpreter → grammar

	for _, m := range matches {
		aliasMap[m.grammar] = m.grammar

		if m.linguistName == "" {
			continue
		}

		displayMap[m.grammar] = m.linguistName
		aliasMap[strings.ToLower(m.linguistName)] = m.grammar

		if entry, ok := langData[m.linguistName]; ok {
			for _, alias := range entry.Aliases {
				aliasMap[strings.ToLower(alias)] = m.grammar
			}
			for _, fn := range entry.Filenames {
				// First grammar to claim a filename wins.
				if _, exists := filenameMap[fn]; !exists {
					filenameMap[fn] = m.grammar
				}
			}
			for _, ext := range entry.Extensions {
				ext = strings.ToLower(ext)
				// First grammar to claim an extension wins.
				if _, exists := extensionMap[ext]; !exists {
					extensionMap[ext] = m.grammar
				}
			}
			for _, interp := range entry.Interpreters {
				interp = strings.ToLower(interp)
				if _, exists := interpreterMap[interp]; !exists {
					interpreterMap[interp] = m.grammar
				}
			}
		}
	}

	// 6. Generate Go source.
	code := generateGoSource(aliasMap, displayMap, filenameMap, extensionMap, interpreterMap)
	if err := os.WriteFile(*outPath, []byte(code), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *outPath, err)
		os.Exit(1)
	}

	matched := 0
	for _, m := range matches {
		if m.linguistName != "" {
			matched++
		}
	}
	fmt.Fprintf(os.Stderr, "generated %s: %d aliases, %d display names, %d filenames, %d extensions, %d interpreters (%d/%d grammars matched)\n",
		*outPath, len(aliasMap), len(displayMap), len(filenameMap), len(extensionMap), len(interpreterMap), matched, len(grammarNames))
}

func parseManifestNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var names []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 1 {
			names = append(names, fields[0])
		}
	}
	return names, sc.Err()
}

func loadLanguagesYML(localPath string) (map[string]linguistEntry, error) {
	var data []byte
	var err error

	if localPath != "" {
		data, err = os.ReadFile(localPath)
	} else {
		fmt.Fprintln(os.Stderr, "fetching languages.yml from GitHub...")
		resp, herr := http.Get(linguistURL)
		if herr != nil {
			return nil, herr
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		data, err = io.ReadAll(resp.Body)
	}
	if err != nil {
		return nil, err
	}

	var result map[string]linguistEntry
	if err := yaml.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	return result, nil
}

func generateGoSource(aliasMap, displayMap, filenameMap, extensionMap, interpreterMap map[string]string) string {
	var b strings.Builder
	b.WriteString("// Code generated by cmd/gen_linguist; DO NOT EDIT.\n")
	b.WriteString("// Re-generate: go run ./cmd/gen_linguist -manifest grammars/languages.manifest\n\n")
	b.WriteString("package grammars\n\n")

	writeMap := func(name, comment string, m map[string]string) {
		fmt.Fprintf(&b, "// %s\n", comment)
		fmt.Fprintf(&b, "var %s = map[string]string{\n", name)
		for _, k := range sortedKeys(m) {
			fmt.Fprintf(&b, "\t%q: %q,\n", k, m[k])
		}
		b.WriteString("}\n\n")
	}

	writeMap("linguistToGrammar",
		"linguistToGrammar maps lowercased linguist names and aliases to gotreesitter grammar names.",
		aliasMap)
	writeMap("grammarDisplayNames",
		"grammarDisplayNames maps gotreesitter grammar names to their linguist canonical display name.",
		displayMap)
	writeMap("linguistFilenames",
		"linguistFilenames maps exact filenames to gotreesitter grammar names.",
		filenameMap)
	writeMap("linguistExtensions",
		"linguistExtensions maps lowercased file extensions to gotreesitter grammar names.",
		extensionMap)
	writeMap("linguistInterpreters",
		"linguistInterpreters maps interpreter names (from shebangs) to gotreesitter grammar names.",
		interpreterMap)

	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
