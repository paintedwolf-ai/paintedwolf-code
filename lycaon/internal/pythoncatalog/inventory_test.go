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
		"Portable Python 3.12.0 (cpython / wasm32-wasi)",
		"PyYAML 6.0.2 (import yaml)",
		"beautifulsoup4 4.12.3 (import bs4) [backend: html.parser]",
		"markdown-it-py 3.0.0 (import markdown_it)",
		"json5 0.17.3 (import json5)",
		"tzdata 2026.6 (import tzdata)",
		"python-dateutil 4.0.0 (import dateutil)",
		"sympy 1.14.0 (import sympy)",
		"Pillow 11.2.1 (import PIL)",
		"lxml 5.4.0 (import lxml, lxml.etree)",
		"python-docx 1.2.0 (import docx)",
		"python-pptx 1.0.2 (import pptx)",
		"fpdf2 2.9.0 (import fpdf)",
		"/packages/fonts/DejaVuSans.ttf",
		"- pytest\n",
		"- markdown-it\n",
		"Standard-library capabilities:",
		ExternalPythonRoute,
	} {
		if !strings.Contains(help, want) {
			t.Errorf("RenderPackageHelp missing %q:\n%s", want, help)
		}
	}
}

func TestRenderPackageExamplesCoversPY10AndBaselineDiscoveries(t *testing.T) {
	t.Parallel()
	m := DefaultLaunchManifest()
	// Include baseline supporting distributions Pygments and charset-normalizer as in built images.
	m.Distributions = append(m.Distributions,
		DistributionRecord{
			Name:           "Pygments",
			NormalizedName: "pygments",
			Version:        "2.19.2",
			Public:         false,
			Imports:        []string{"pygments"},
			DistInfoPath:   "packages/pygments-2.19.2.dist-info",
		},
		DistributionRecord{
			Name:           "charset-normalizer",
			NormalizedName: "charset-normalizer",
			Version:        "3.4.3",
			Public:         false,
			Imports:        []string{"charset_normalizer"},
			DistInfoPath:   "packages/charset_normalizer-3.4.3.dist-info",
		},
	)
	all := RenderPackageExamples(m, "")
	for _, want := range []string{
		"(json5)",
		"(tzdata-zoneinfo)",
		"(python-dateutil)",
		"(sympy)",
		"(pillow)",
		"(lxml)",
		"(python-docx)",
		"(python-pptx)",
		"(fpdf2)",
		"(pygments)",
		"(charset-normalizer)",
		"(sqlite3-join)",
		"(openpyxl-chart)",
		"/packages/fonts/DejaVuSans.ttf",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("RenderPackageExamples missing %q:\n%s", want, all)
		}
	}

	// Task-oriented query lookup
	imgEx := RenderPackageExamples(m, "resize an image")
	if !strings.Contains(imgEx, "(pillow)") || strings.Contains(imgEx, "(sympy)") {
		t.Fatalf("unexpected task query result for 'resize an image':\n%s", imgEx)
	}
	deckEx := RenderPackageExamples(m, "write a slide deck")
	if !strings.Contains(deckEx, "(python-pptx)") || strings.Contains(deckEx, "(fpdf2)") {
		t.Fatalf("unexpected task query result for 'write a slide deck':\n%s", deckEx)
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

	// Missing distribution: numpy / pandas must point to csv, sqlite3, statistics and declared project runner
	for _, pkg := range []string{"numpy", "pandas"} {
		d = DiagnoseImport(m, pkg)
		formatted := d.Format()
		if d.Kind != DiagnosticMissingDistribution ||
			!strings.Contains(formatted, "csv") ||
			!strings.Contains(formatted, "sqlite3") ||
			!strings.Contains(formatted, "statistics") ||
			!strings.Contains(formatted, "declared project runner") ||
			!strings.Contains(formatted, ExternalPythonRoute) {
			t.Fatalf("DiagnoseImport(%s) = %+v (formatted: %q)", pkg, d, formatted)
		}
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
