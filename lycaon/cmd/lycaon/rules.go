package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

func runRules(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pw rules test [--project DIR] [--json] [PATH…]")
	}
	switch args[0] {
	case "test":
		return runRulesTest(args[1:])
	default:
		return fmt.Errorf("unknown rules command %q (want test)", args[0])
	}
}

type rulesTestFlags struct {
	projectDir string
	jsonOut    bool
	paths      []string
}

func parseRulesTestFlags(args []string) (rulesTestFlags, error) {
	var f rulesTestFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			f.jsonOut = true
		case "--project":
			dir, ok := nextArg(args, i)
			if !ok {
				return f, fmt.Errorf("--project requires a directory")
			}
			f.projectDir = dir
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				return f, fmt.Errorf("unknown flag %q", args[i])
			}
			f.paths = append(f.paths, args[i])
		}
	}
	return f, nil
}

func runRulesTest(args []string) error {
	flags, err := parseRulesTestFlags(args)
	if err != nil {
		return err
	}
	moduleRoot := configlayout.FindModuleRoot()
	// Declared scenarios render through the same renderer the host installs.
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(
		prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
			ModuleRoot: moduleRoot,
			Site:       prompts.SitePromptFilesDir(moduleRoot),
		})))

	paths := flags.paths
	if len(paths) == 0 {
		project := strings.TrimSpace(flags.projectDir)
		if project == "" {
			project, err = os.Getwd()
			if err != nil {
				return err
			}
		}
		paths = defaultRulePaths(project)
		if len(paths) == 0 {
			return fmt.Errorf("no rules found under %s — pass a rule file or a pack policy/ directory", project)
		}
	}

	// Without a checkout the bundled catalog and schemas answer.
	schemaDir := configlayout.SchemasDir(moduleRoot)
	anchorCatalog := ""
	if configlayout.IsModuleRoot(moduleRoot) {
		anchorCatalog = filepath.Join(moduleRoot, "config", "packs", "painted-wolf",
			"platform", "host", "anchors", "catalog.yaml")
	}
	if schemaDir == "" {
		fmt.Fprintln(os.Stderr, "pw: no checkout beside this folder — rules compile against bundled schemas")
	}
	results, err := oar.CheckRuleFiles(paths, oar.RuleCheckOptions{
		SchemaDir:         schemaDir,
		AnchorCatalogPath: anchorCatalog,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pw: %v\n", err)
		os.Exit(2)
	}

	failed := 0
	for _, r := range results {
		if !r.OK() {
			failed++
		}
	}

	if flags.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	} else {
		for _, r := range results {
			id := r.RuleID
			if id == "" {
				id = r.Path
			}
			if r.OK() {
				fmt.Printf("ok   %-46s %d scenario(s)\n", id, r.Scenarios)
				continue
			}
			fmt.Printf("FAIL %-46s %s\n", id, r.Code)
			if strings.TrimSpace(r.Detail) != "" {
				fmt.Printf("       %s\n", strings.ReplaceAll(strings.TrimSpace(r.Detail), "\n", "\n       "))
			}
		}
		fmt.Printf("rules test: %d rule(s), %d failed\n", len(results), failed)
	}

	if failed > 0 {
		os.Exit(1)
	}
	return nil
}

// defaultRulePaths finds the policy trees a project contributes: its own
// overlay rules plus the policy directory of any pack checked out inside it.
// An author working in their pack repo can run the verb with no arguments.
func defaultRulePaths(projectDir string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	add(filepath.Join(projectDir, settingsoverlay.DirName(), "policy"))
	add(filepath.Join(projectDir, "policy"))
	ents, err := os.ReadDir(projectDir)
	if err != nil {
		return out
	}
	for _, ent := range ents {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectDir, ent.Name(), "extension.yaml")); err != nil {
			continue
		}
		add(filepath.Join(projectDir, ent.Name(), "policy"))
	}
	return out
}

// nextArg returns the value following the flag at i, or false when the flag was
// given without one.
func nextArg(args []string, i int) (string, bool) {
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}
