package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/configlayout"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/internal/workflowvalidate"
	"github.com/lycaon/lycaon/pkg/api"
)

func runWorkflow(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pw workflow validate [--bundled] [--project DIR] [--json] [--strict-merchandising] [PATH…]")
	}
	switch args[0] {
	case "validate":
		return runWorkflowValidate(ctx, args[1:])
	default:
		return fmt.Errorf("unknown workflow command %q (want validate)", args[0])
	}
}

type validateCLIFlags struct {
	bundled             bool
	jsonOut             bool
	strictMerchandising bool
	projectDir          string
	paths               []string
}

func parseValidateFlags(args []string) (validateCLIFlags, error) {
	var f validateCLIFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--bundled":
			f.bundled = true
		case "--json":
			f.jsonOut = true
		case "--strict-merchandising":
			f.strictMerchandising = true
		case "--project":
			if i+1 >= len(args) {
				return f, fmt.Errorf("--project requires a directory")
			}
			f.projectDir = args[i+1]
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

// cliDiagnostic is the --json envelope (CLI-only; not pkg/api).
type cliDiagnostic struct {
	Path        string `json:"path,omitempty"`
	Field       string `json:"field"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Replacement string `json:"replacement,omitempty"`
}

type cliValidateResult struct {
	Errors   []cliDiagnostic `json:"errors"`
	Warnings []cliDiagnostic `json:"warnings,omitempty"`
}

func runWorkflowValidate(ctx context.Context, args []string) error {
	flags, err := parseValidateFlags(args)
	if err != nil {
		return err
	}
	moduleRoot := configlayout.FindModuleRoot()

	opts := workflowvalidate.CatalogValidateOptions{
		ConfigRoot: moduleRoot,
		ProjectDir: flags.projectDir,
	}
	switch {
	case flags.bundled:
		opts.Mode = workflowvalidate.ModeBundled
	case len(flags.paths) > 0:
		opts.Mode = workflowvalidate.ModePaths
		absPaths := make([]string, 0, len(flags.paths))
		for _, p := range flags.paths {
			abs, err := filepath.Abs(p)
			if err != nil {
				return err
			}
			absPaths = append(absPaths, abs)
		}
		opts.Paths = absPaths
	default:
		opts.Mode = workflowvalidate.ModeProject
		if opts.ProjectDir == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			opts.ProjectDir = cwd
		}
	}

	diags, err := workflowvalidate.ValidateCatalog(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pw: %v\n", err)
		os.Exit(2)
	}

	var errors, warnings []api.ComposeValidationError
	for _, d := range diags {
		if d.Code == string(workflowdiag.MustCode("merchandising_incomplete")) && !flags.strictMerchandising {
			warnings = append(warnings, d)
			continue
		}
		errors = append(errors, d)
	}

	if flags.jsonOut {
		out := cliValidateResult{
			Errors:   toCLIDiags(errors),
			Warnings: toCLIDiags(warnings),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		for _, d := range errors {
			printHumanDiag(d)
		}
		for _, d := range warnings {
			fmt.Fprintf(os.Stderr, "warning: ")
			printHumanDiag(d)
		}
		fmt.Printf("workflow validate: %d error(s), %d warning(s)\n", len(errors), len(warnings))
	}

	if len(errors) > 0 {
		os.Exit(1)
	}
	return nil
}

func toCLIDiags(in []api.ComposeValidationError) []cliDiagnostic {
	out := make([]cliDiagnostic, 0, len(in))
	for _, d := range in {
		path, field := splitFieldPath(d.Field)
		out = append(out, cliDiagnostic{
			Path:        path,
			Field:       field,
			Code:        d.Code,
			Message:     d.Message,
			Replacement: d.Replacement,
		})
	}
	return out
}

func splitFieldPath(field string) (path, rest string) {
	if i := strings.Index(field, ": "); i >= 0 {
		return field[:i], field[i+2:]
	}
	return "", field
}

func printHumanDiag(d api.ComposeValidationError) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", d.Field, d.Code)
	if d.Message != "" {
		fmt.Fprintf(os.Stderr, "  %s\n", d.Message)
	}
	if d.Replacement != "" {
		fmt.Fprintf(os.Stderr, "  Instead: %s\n", d.Replacement)
	}
}
