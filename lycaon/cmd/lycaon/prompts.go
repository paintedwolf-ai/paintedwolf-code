package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/prompts"
)

func runPrompts(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pw prompts render <template-ref> [--agent ID] [--skill NAME] [--resource PATH] [--surface ID] [--project DIR] [--var k=v]… [--check] [--json]")
	}
	switch args[0] {
	case "render":
		return runPromptsRender(ctx, args[1:])
	default:
		return fmt.Errorf("unknown prompts command %q (want render)", args[0])
	}
}

type promptsRenderFlags struct {
	agentID    string
	projectDir string
	skillName  string
	resource   string
	surfaceID  string
	vars       map[string]any
	check      bool
	jsonOut    bool
	ref        string
}

func parsePromptsRenderFlags(args []string) (promptsRenderFlags, error) {
	f := promptsRenderFlags{vars: map[string]any{}}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			f.check = true
		case "--json":
			f.jsonOut = true
		case "--agent":
			id, ok := nextArg(args, i)
			if !ok {
				return f, fmt.Errorf("--agent requires an agent id")
			}
			f.agentID = id
			i++
		case "--project":
			dir, ok := nextArg(args, i)
			if !ok {
				return f, fmt.Errorf("--project requires a directory")
			}
			f.projectDir = dir
			i++
		case "--skill":
			name, ok := nextArg(args, i)
			if !ok {
				return f, fmt.Errorf("--skill requires a skill name")
			}
			f.skillName = name
			i++
		case "--resource":
			path, ok := nextArg(args, i)
			if !ok {
				return f, fmt.Errorf("--resource requires a skill-relative path")
			}
			f.resource = path
			i++
		case "--surface":
			id, ok := nextArg(args, i)
			if !ok {
				return f, fmt.Errorf("--surface requires a surface id")
			}
			f.surfaceID = id
			i++
		case "--var":
			raw, ok := nextArg(args, i)
			if !ok {
				return f, fmt.Errorf("--var requires k=v")
			}
			key, value, ok := strings.Cut(raw, "=")
			if !ok || strings.TrimSpace(key) == "" {
				return f, fmt.Errorf("--var %q is not k=v", raw)
			}
			var typed any
			if err := json.Unmarshal([]byte(value), &typed); err != nil {
				typed = value
			}
			f.vars[strings.TrimSpace(key)] = typed
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				return f, fmt.Errorf("unknown flag %q", args[i])
			}
			if f.ref != "" {
				return f, fmt.Errorf("only one template ref may be given (got %q and %q)", f.ref, args[i])
			}
			f.ref = args[i]
		}
	}
	if f.ref != "" && f.skillName != "" {
		return f, fmt.Errorf("--skill and a template ref are mutually exclusive")
	}
	if f.skillName == "" {
		if f.resource != "" {
			return f, fmt.Errorf("--resource requires --skill")
		}
		if f.surfaceID != "" {
			return f, fmt.Errorf("--surface requires --skill")
		}
	}
	return f, nil
}

// promptsRenderJSON is the --json envelope (CLI-only; not pkg/api).
type promptsRenderJSON struct {
	Output     string   `json:"output"`
	Violations []string `json:"violations,omitempty"`
	Revision   string   `json:"revision"`
}

func runPromptsRender(ctx context.Context, args []string) error {
	flags, err := parsePromptsRenderFlags(args)
	if err != nil {
		return err
	}
	moduleRoot := configlayout.FindModuleRoot()
	projectDir := strings.TrimSpace(flags.projectDir)
	if projectDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			projectDir = cwd
		}
	}
	if flags.check && flags.skillName == "" && strings.TrimSpace(flags.agentID) == "" {
		fmt.Fprintln(os.Stderr, "pw: --check only checks persona contracts; pass --agent to enforce them")
	}

	res, err := prompts.AuthorRender(ctx, prompts.AuthorRenderRequest{
		ModuleRoot:  moduleRoot,
		ProjectDir:  projectDir,
		TemplateRef: flags.ref,
		AgentID:     flags.agentID,
		SkillName:   flags.skillName,
		Resource:    flags.resource,
		SurfaceID:   flags.surfaceID,
		Vars:        flags.vars,
		Check:       flags.check,
	})
	if err != nil {
		return err
	}

	if flags.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(promptsRenderJSON{Output: res.Output, Violations: res.Violations, Revision: res.Revision}); err != nil {
			return err
		}
	} else {
		fmt.Print(res.Output)
		if !strings.HasSuffix(res.Output, "\n") {
			fmt.Println()
		}
		for _, v := range res.Violations {
			fmt.Fprintf(os.Stderr, "violation: %s\n", v)
		}
	}
	if len(res.Violations) > 0 {
		os.Exit(1)
	}
	return nil
}
