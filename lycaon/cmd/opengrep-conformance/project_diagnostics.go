package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/pkg/api"
)

func validateProjectDiagnostics(project projectCase) error {
	if len(project.Diagnostics) == 0 {
		return nil
	}
	for mode := range project.Diagnostics {
		if mode != opengrep.Intraprocedural && mode != opengrep.Intrafile {
			return fmt.Errorf("%s: unknown diagnostic analysis mode %q", project.Name, mode)
		}
	}
	for _, mode := range []opengrep.Mode{opengrep.Intraprocedural, opengrep.Intrafile} {
		diagnostics, exists := project.Diagnostics[mode]
		if !exists {
			return fmt.Errorf("%s: missing diagnostic expectations for %s", project.Name, mode)
		}
		if err := validateProjectDiagnosticRows(project, diagnostics); err != nil {
			return err
		}
	}
	return nil
}

func compareProjectDiagnostics(project projectCase, mode opengrep.Mode, actual []expectedProjectDiagnostic) error {
	expected := sortedProjectDiagnostics(project.Diagnostics[mode])
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("%s diagnostics want %+v; got %+v", mode, expected, actual)
	}
	return nil
}

func validateProjectDiagnosticRows(project projectCase, diagnostics []expectedProjectDiagnostic) error {
	seen := make(map[expectedProjectDiagnostic]bool)
	for _, diagnostic := range diagnostics {
		if seen[diagnostic] {
			return fmt.Errorf("%s: duplicate expected diagnostic", project.Name)
		}
		seen[diagnostic] = true
		switch diagnostic.Kind {
		case api.ScanWarningRuleParseError, api.ScanWarningFilePartialParse, api.ScanWarningFilePartialSemantics, api.ScanWarningTargetUnscanned:
		default:
			return fmt.Errorf("%s: unknown diagnostic kind %q", project.Name, diagnostic.Kind)
		}
		source, exists := project.Files[diagnostic.File]
		if diagnostic.File != "" && !exists {
			return fmt.Errorf("%s: diagnostic refers to an unknown source file", project.Name)
		}
		if diagnostic.Line < 0 || diagnostic.Column < 0 || (diagnostic.Line == 0) != (diagnostic.Column == 0) ||
			(diagnostic.Line > 0 && (!exists || diagnostic.Line > strings.Count(source, "\n")+1)) {
			return fmt.Errorf("%s: diagnostic has invalid source coordinates", project.Name)
		}
		if diagnostic.Kind == api.ScanWarningFilePartialSemantics && diagnostic.Construct == "" {
			return fmt.Errorf("%s: semantic diagnostic requires a construct", project.Name)
		}
	}
	return nil
}
