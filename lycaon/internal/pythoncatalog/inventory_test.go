package pythoncatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExecuteReadOnlyPipInventoryForms(t *testing.T) {
	t.Parallel()
	m := DefaultLaunchManifest()

	// list (default columns)
	res, err := ExecuteReadOnlyPip(m, []string{"list"})
	if err != nil {
		t.Fatalf("pip list: %v", err)
	}
	if res.ExitStatus != 0 || !strings.Contains(res.Stdout, "Package") || !strings.Contains(res.Stdout, "PyYAML") {
		t.Fatalf("unexpected pip list result: %+v", res)
	}

	// list --format=json and --format json
	for _, args := range [][]string{{"list", "--format=json"}, {"list", "--format", "json"}} {
		res, err := ExecuteReadOnlyPip(m, args)
		if err != nil || res.ExitStatus != 0 {
			t.Fatalf("pip %v: err=%v res=%+v", args, err, res)
		}
		var rows []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal([]byte(res.Stdout), &rows); err != nil {
			t.Fatalf("unmarshal pip list json: %v", err)
		}
		if len(rows) != LaunchPublicDistributionCount {
			t.Fatalf("json rows = %d, want %d", len(rows), LaunchPublicDistributionCount)
		}
	}

	// list --format=freeze and freeze
	for _, args := range [][]string{{"list", "--format=freeze"}, {"freeze"}} {
		res, err := ExecuteReadOnlyPip(m, args)
		if err != nil || res.ExitStatus != 0 {
			t.Fatalf("pip %v: err=%v res=%+v", args, err, res)
		}
		if !strings.Contains(res.Stdout, "PyYAML==6.0.2\n") || !strings.Contains(res.Stdout, "openpyxl==3.1.5\n") {
			t.Fatalf("unexpected freeze output:\n%s", res.Stdout)
		}
	}

	// show NAME (normalized lookup + multiple packages)
	res, err = ExecuteReadOnlyPip(m, []string{"show", "pyyaml", "ruamel_yaml"})
	if err != nil || res.ExitStatus != 0 {
		t.Fatalf("pip show: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Stdout, "Name: PyYAML\n") || !strings.Contains(res.Stdout, "---\nName: ruamel.yaml\n") {
		t.Fatalf("unexpected pip show output:\n%s", res.Stdout)
	}

	// show missing package
	res, err = ExecuteReadOnlyPip(m, []string{"show", "numpy"})
	if err != nil || res.ExitStatus != 1 || res.Diagnostic == nil || res.Diagnostic.Kind != DiagnosticMissingDistribution {
		t.Fatalf("expected missing distribution diagnostic, got err=%v res=%+v", err, res)
	}

	// show partial hit + miss
	res, err = ExecuteReadOnlyPip(m, []string{"show", "PyYAML", "numpy"})
	if err != nil || res.ExitStatus != 0 || !strings.Contains(res.Stdout, "Name: PyYAML") || !strings.Contains(res.Stderr, "not found: numpy") {
		t.Fatalf("unexpected partial show result: err=%v res=%+v", err, res)
	}
}

func TestExecuteReadOnlyPipMutationAndErrors(t *testing.T) {
	t.Parallel()
	m := DefaultLaunchManifest()

	for _, sub := range []string{"install", "uninstall", "download", "wheel", "upgrade"} {
		res, err := ExecuteReadOnlyPip(m, []string{sub, "numpy"})
		if err != nil {
			t.Fatalf("pip %s: %v", sub, err)
		}
		if res.ExitStatus != 1 || res.Diagnostic == nil || res.Diagnostic.Kind != DiagnosticImageMutation {
			t.Fatalf("pip %s expected image_mutation diagnostic, got %+v", sub, res)
		}
		if !strings.Contains(res.Stderr, ExternalPythonRoute) {
			t.Fatalf("pip %s stderr missing external route: %q", sub, res.Stderr)
		}
	}

	// Empty args, unknown subcommand, bad flags
	for _, args := range [][]string{
		{},
		{"check"},
		{"list", "--outdated"},
		{"list", "--format=invalid"},
		{"show"},
		{"show", "--files"},
		{"freeze", "--all"},
	} {
		res, err := ExecuteReadOnlyPip(m, args)
		if err != nil || res.ExitStatus == 0 {
			t.Fatalf("pip %v expected non-zero exit without fatal error, got err=%v res=%+v", args, err, res)
		}
	}

	// Invalid manifest returns error
	if _, err := ExecuteReadOnlyPip(ImageManifest{}, []string{"list"}); err == nil {
		t.Fatal("expected error for invalid manifest")
	}
}

func TestRenderPackageHelp(t *testing.T) {
	t.Parallel()
	m := DefaultLaunchManifest()
	help := RenderPackageHelp(m)
	for _, want := range []string{
		"Portable Python 3.12.8",
		"PyYAML 6.0.2 (import yaml)",
		"beautifulsoup4 4.12.3 (import bs4) [backend: html.parser]",
		"markdown-it-py 3.0.0 (import markdown_it)",
		"- pytest (pytest:console_main)",
		"- markdown-it (markdown_it.cli.parse:interactive)",
		"Read, join and emit structured records",
		ExternalPythonRoute,
	} {
		if !strings.Contains(help, want) {
			t.Errorf("RenderPackageHelp missing %q:\n%s", want, help)
		}
	}
}

func TestDiagnosticsDistinguishFailureClasses(t *testing.T) {
	t.Parallel()
	m := DefaultLaunchManifest()

	// Wrong import name: distribution name passed as import
	d := DiagnoseImport(m, "PyYAML")
	if d.Kind != DiagnosticWrongImportName || !strings.Contains(d.Format(), "yaml") {
		t.Fatalf("DiagnoseImport(PyYAML) = %+v", d)
	}

	// Wrong import name: legacy alias
	d = DiagnoseImport(m, "PyPDF2")
	if d.Kind != DiagnosticWrongImportName || !strings.Contains(d.Format(), "pypdf") {
		t.Fatalf("DiagnoseImport(PyPDF2) = %+v", d)
	}

	// Wrong import name: wrong casing
	d = DiagnoseImport(m, "BS4")
	if d.Kind != DiagnosticWrongImportName || !strings.Contains(d.Format(), "bs4") {
		t.Fatalf("DiagnoseImport(BS4) = %+v", d)
	}

	// Missing distribution
	d = DiagnoseImport(m, "pandas")
	if d.Kind != DiagnosticMissingDistribution || !strings.Contains(d.Format(), ExternalPythonRoute) {
		t.Fatalf("DiagnoseImport(pandas) = %+v", d)
	}

	// Empty import
	d = DiagnoseImport(m, "")
	if d.Kind != DiagnosticMissingDistribution {
		t.Fatalf("DiagnoseImport('') = %+v", d)
	}

	// Unavailable extra/backend: lxml -> names html.parser
	d = DiagnoseExtraOrBackend(m, "beautifulsoup4", "lxml")
	if d.Kind != DiagnosticUnavailableExtra || !strings.Contains(d.Format(), "html.parser") {
		t.Fatalf("DiagnoseExtraOrBackend(beautifulsoup4, lxml) = %+v", d)
	}

	// Unavailable extra on another installed distribution
	d = DiagnoseExtraOrBackend(m, "pypdf", "crypto")
	if d.Kind != DiagnosticUnavailableExtra || !strings.Contains(d.Format(), "pure-python") {
		t.Fatalf("DiagnoseExtraOrBackend(pypdf, crypto) = %+v", d)
	}

	// Unavailable extra on unknown distribution
	d = DiagnoseExtraOrBackend(m, "unknownpkg", "extra")
	if d.Kind != DiagnosticUnavailableExtra || !strings.Contains(d.Format(), ExternalPythonRoute) {
		t.Fatalf("DiagnoseExtraOrBackend(unknownpkg, extra) = %+v", d)
	}

	// Unknown CLI
	d = DiagnoseCLI(m, "black")
	if d.Kind != DiagnosticUnknownCLI || !strings.Contains(d.Format(), "pytest") || !strings.Contains(d.Format(), "markdown-it") {
		t.Fatalf("DiagnoseCLI(black) = %+v", d)
	}

	// Image mutation
	d = DiagnoseMutation("pip install requests")
	if d.Kind != DiagnosticImageMutation || !strings.Contains(d.Format(), ExternalPythonRoute) {
		t.Fatalf("DiagnoseMutation = %+v", d)
	}
}
