package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/prompts"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"gopkg.in/yaml.v3"
)

func runPack(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pw pack test [DIR]")
	}
	switch args[0] {
	case "test":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		return runPackTest(ctx, dir)
	default:
		return fmt.Errorf("unknown pack command %q (want test)", args[0])
	}
}

type packReplayFixture struct {
	WorkflowID      string `yaml:"workflow_id"`
	WorkflowVersion string `yaml:"workflow_version"`
	TerminalStatus  string `yaml:"terminal_status"`
}

type packChangesDoc struct {
	Changes []struct {
		ID       string `yaml:"id"`
		Category string `yaml:"category"`
	} `yaml:"changes"`
}

func runPackTest(ctx context.Context, dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve directory %s: %w", dir, err)
	}

	info, err := os.Stat(absDir)
	if err != nil {
		return fmt.Errorf("stat %s: %w", absDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", absDir)
	}

	var workflowFiles []string
	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "workflow.yaml" {
			workflowFiles = append(workflowFiles, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", absDir, err)
	}

	if len(workflowFiles) == 0 {
		return fmt.Errorf("no workflow.yaml manifests found under %s", absDir)
	}

	fmt.Printf("Testing pack at %s (%d workflow manifests found)\n", absDir, len(workflowFiles))

	// 1. Validate manifests with strict KnownFields and format normalization
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	budgets, _ := prompts.LoadPromptBudgets()

	for _, wfPath := range workflowFiles {
		relWf, _ := filepath.Rel(absDir, wfPath)
		data, err := os.ReadFile(wfPath)
		if err != nil {
			return fmt.Errorf("%s: read error: %w", relWf, err)
		}

		m, err := workflowdef.ParseManifestYAML(data)
		if err != nil {
			return fmt.Errorf("%s: manifest parse error: %w", relWf, err)
		}

		if m.ID == "" || m.Version == "" {
			return fmt.Errorf("%s: id and version required", relWf)
		}
		if len(m.PhaseDefs) == 0 {
			return fmt.Errorf("%s: phase definitions required", relWf)
		}
		fmt.Printf("  ✓ manifest load: %s@%s (%s)\n", m.ID, m.Version, relWf)

		// 2. Validate referenced prompts and check budgets
		for _, inject := range m.Injects {
			if inject.Render != "" {
				promptFile := filepath.Join(filepath.Dir(wfPath), "guidance", inject.Render+".md")
				if _, err := os.Stat(promptFile); err == nil {
					rendered, err := engine.Render(ctx, promptFile, map[string]any{})
					if err != nil {
						return fmt.Errorf("%s inject render %s: %w", relWf, inject.Render, err)
					}
					if budgets != nil {
						if cap, ok := budgets.Kicks[inject.Render]; ok && len(rendered) > cap {
							return fmt.Errorf("%s prompt %s rendered bytes %d exceeds cap %d", relWf, inject.Render, len(rendered), cap)
						}
					}
					fmt.Printf("  ✓ prompt render: %s\n", inject.Render)
				}
			}
		}
	}

	// 3. Validate replay fixtures and changes.yaml if present
	replayDir := filepath.Join(absDir, "testdata", "replay")
	if replayEntries, err := os.ReadDir(replayDir); err == nil {
		for _, re := range replayEntries {
			if !re.IsDir() {
				continue
			}
			fixDir := filepath.Join(replayDir, re.Name())
			replayFile := filepath.Join(fixDir, "replay.yaml")
			if replayData, err := os.ReadFile(replayFile); err == nil {
				var fixture packReplayFixture
				if err := yaml.Unmarshal(replayData, &fixture); err != nil {
					return fmt.Errorf("%s: unmarshal replay.yaml: %w", fixDir, err)
				}
				changesFile := filepath.Join(fixDir, "changes.yaml")
				if changeData, err := os.ReadFile(changesFile); err == nil {
					var doc packChangesDoc
					if err := yaml.Unmarshal(changeData, &doc); err != nil {
						return fmt.Errorf("%s: unmarshal changes.yaml: %w", fixDir, err)
					}
					for _, c := range doc.Changes {
						if c.Category != "spec_fix" && c.Category != "safety" {
							return fmt.Errorf("%s: invalid change category %q (only spec_fix and safety permitted)", fixDir, c.Category)
						}
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("%s: read changes.yaml: %w", fixDir, err)
				}
				fmt.Printf("  ✓ replay fixture validated: %s\n", re.Name())
			}
		}
	}

	fmt.Println("Pack tests passed.")
	return nil
}
