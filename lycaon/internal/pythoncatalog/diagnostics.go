package pythoncatalog

import (
	"fmt"
	"strings"
)

// DiagnosticKind classifies an actionable Portable Python catalog failure.
type DiagnosticKind string

const (
	DiagnosticMissingDistribution DiagnosticKind = "missing_distribution"
	DiagnosticWrongImportName     DiagnosticKind = "wrong_import_name"
	DiagnosticUnavailableExtra    DiagnosticKind = "unavailable_extra"
	DiagnosticUnknownCLI          DiagnosticKind = "unknown_cli"
	DiagnosticImageMutation       DiagnosticKind = "image_mutation"
)

// Diagnostic is a structured, actionable failure message derived from ImageManifest.
type Diagnostic struct {
	Kind          DiagnosticKind `json:"kind"`
	Requested     string         `json:"requested"`
	Message       string         `json:"message"`
	Available     []string       `json:"available,omitempty"`
	Suggestion    string         `json:"suggestion,omitempty"`
	ExternalRoute string         `json:"external_route,omitempty"`
}

// Format renders the diagnostic as concise text for stderr or tool feedback.
func (d Diagnostic) Format() string {
	var b strings.Builder
	b.WriteString(d.Message)
	if len(d.Available) > 0 {
		fmt.Fprintf(&b, " Available: %s.", strings.Join(d.Available, ", "))
	}
	if d.Suggestion != "" {
		fmt.Fprintf(&b, " %s", d.Suggestion)
	}
	if d.ExternalRoute != "" {
		fmt.Fprintf(&b, " For system or project environments, use `%s`.", d.ExternalRoute)
	}
	return b.String()
}

// legacyImportAliases maps common wrong/legacy import spellings to the shipped import name.
var legacyImportAliases = map[string]string{
	"pyyaml":             "yaml",
	"beautifulsoup4":     "bs4",
	"beautifulsoup":      "bs4",
	"markdown-it-py":     "markdown_it",
	"markdown_it_py":     "markdown_it",
	"pypdf2":             "pypdf",
	"ruamel_yaml":        "ruamel.yaml",
	"python_dateutil":    "dateutil",
	"python_docx":        "docx",
	"python_pptx":        "pptx",
	"etree":              "lxml.etree",
	"pyjwt":              "jwt",
	"python_dotenv":      "dotenv",
	"license-expression": "license_expression",
	"graphql-core":       "graphql",
	"python_hcl2":        "hcl2",
}

// portableNumericOutOfScope lists scientific/plotting packages outside the
// cumulative image (where numpy and pandas ARE installed), pointing to numpy,
// pandas, openpyxl workbook charts, or pygal standalone SVG charts.
var portableNumericOutOfScope = map[string]bool{
	"scipy":      true,
	"polars":     true,
	"matplotlib": true,
}

var portableHTTPOutOfScope = map[string]bool{
	"httpx":   true,
	"aiohttp": true,
}

var portableJSONOutOfScope = map[string]bool{
	"orjson": true,
	"ujson":  true,
}

var portableFuzzyOutOfScope = map[string]bool{
	"rapidfuzz":   true,
	"thefuzz":     true,
	"fuzzywuzzy":  true,
	"levenshtein": true,
}

var portableKeccakOutOfScope = map[string]bool{
	"eth_hash":     true,
	"eth-hash":     true,
	"pysha3":       true,
	"keccak":       true,
	"pycryptodome": true,
}

// DiagnoseImport distinguishes wrong import names (when the distribution is installed under
// a different import spelling) from genuinely missing distributions.
func DiagnoseImport(manifest ImageManifest, requested string) Diagnostic {
	trimmed := strings.TrimSpace(requested)
	if trimmed == "" {
		return Diagnostic{
			Kind:          DiagnosticMissingDistribution,
			Requested:     requested,
			Message:       "Empty module or distribution name.",
			ExternalRoute: ExternalPythonRoute,
		}
	}
	// Check if caller passed a distribution name instead of its Python import name.
	if dist, ok := manifest.FindDistribution(trimmed); ok {
		return Diagnostic{
			Kind:       DiagnosticWrongImportName,
			Requested:  trimmed,
			Message:    fmt.Sprintf("%q is the distribution name for %s %s, not its Python import name.", trimmed, dist.Name, dist.Version),
			Available:  dist.Imports,
			Suggestion: fmt.Sprintf("Import %q instead.", dist.Imports[0]),
		}
	}
	// Check known legacy or misspelled import names.
	lower := strings.ToLower(trimmed)
	if targetImport, ok := legacyImportAliases[lower]; ok {
		if dist, found := manifest.FindByImport(targetImport); found {
			return Diagnostic{
				Kind:       DiagnosticWrongImportName,
				Requested:  trimmed,
				Message:    fmt.Sprintf("Import %q is not provided; distribution %s %s exposes %q.", trimmed, dist.Name, dist.Version, targetImport),
				Available:  dist.Imports,
				Suggestion: fmt.Sprintf("Use `import %s`.", targetImport),
			}
		}
	}
	// Check case-insensitive import mismatch (e.g. Jinja2 vs jinja2).
	for _, dist := range manifest.Distributions {
		for _, imp := range dist.Imports {
			if strings.EqualFold(imp, trimmed) && imp != trimmed {
				return Diagnostic{
					Kind:       DiagnosticWrongImportName,
					Requested:  trimmed,
					Message:    fmt.Sprintf("Import %q has wrong casing; distribution %s %s exposes %q.", trimmed, dist.Name, dist.Version, imp),
					Available:  dist.Imports,
					Suggestion: fmt.Sprintf("Use `import %s`.", imp),
				}
			}
		}
	}
	if portableNumericOutOfScope[lower] {
		return Diagnostic{
			Kind:          DiagnosticMissingDistribution,
			Requested:     trimmed,
			Message:       fmt.Sprintf("Package or module %q is not installed in the fixed Portable Python image.", trimmed),
			Available:     []string{"numpy", "pandas", "openpyxl", "pygal"},
			Suggestion:    "For portable numerical and tabular analysis, use installed `numpy` and `pandas` (or stdlib sqlite3, csv, statistics; sympy for exact symbolic math), `openpyxl` for workbook charts, or `pygal` for standalone SVG charts; for a project's own dependencies, use the declared project runner.",
			ExternalRoute: ExternalPythonRoute,
		}
	}
	if portableHTTPOutOfScope[lower] {
		return Diagnostic{
			Kind:          DiagnosticMissingDistribution,
			Requested:     trimmed,
			Message:       fmt.Sprintf("Package or module %q is not installed in the fixed Portable Python image.", trimmed),
			Available:     []string{"requests"},
			Suggestion:    "Use the host-mediated `requests` client for authorized synchronous HTTPS requests.",
			ExternalRoute: ExternalPythonRoute,
		}
	}
	if portableJSONOutOfScope[lower] {
		return Diagnostic{
			Kind:          DiagnosticMissingDistribution,
			Requested:     trimmed,
			Message:       fmt.Sprintf("Package or module %q is not installed in the fixed Portable Python image.", trimmed),
			Available:     []string{"json"},
			Suggestion:    "Use stdlib `json` (or `json5` for JSON5 with comments/trailing commas).",
			ExternalRoute: ExternalPythonRoute,
		}
	}
	if portableFuzzyOutOfScope[lower] {
		return Diagnostic{
			Kind:          DiagnosticMissingDistribution,
			Requested:     trimmed,
			Message:       fmt.Sprintf("Package or module %q is not installed in the fixed Portable Python image.", trimmed),
			Available:     []string{"difflib"},
			Suggestion:    "For basic sequence similarity, use stdlib `difflib` (note: SequenceMatcher semantics and scoring differ from Levenshtein/edit-distance libraries).",
			ExternalRoute: ExternalPythonRoute,
		}
	}
	if lower == "scapy" {
		return Diagnostic{
			Kind:          DiagnosticMissingDistribution,
			Requested:     trimmed,
			Message:       fmt.Sprintf("Package or module %q is not installed in the fixed Portable Python image.", trimmed),
			Available:     []string{"dpkt"},
			Suggestion:    "For offline pcap/pcapng capture reading, use `dpkt` (Scapy is not included in the fixed Portable Python image).",
			ExternalRoute: ExternalPythonRoute,
		}
	}
	if lower == "yamllint" {
		return Diagnostic{
			Kind:          DiagnosticMissingDistribution,
			Requested:     trimmed,
			Message:       fmt.Sprintf("Package or module %q is omitted from the fixed Portable Python image.", trimmed),
			Available:     []string{"ruamel.yaml"},
			Suggestion:    "For duplicate YAML key checks with line numbers, use strict `ruamel.yaml` (`allow_duplicate_keys = False` and `exc.problem_mark.line + 1`); for full yamllint rules, use the declared project runner.",
			ExternalRoute: ExternalPythonRoute,
		}
	}
	if portableKeccakOutOfScope[lower] {
		return Diagnostic{
			Kind:          DiagnosticMissingDistribution,
			Requested:     trimmed,
			Message:       fmt.Sprintf("Package or module %q is not installed in the fixed Portable Python image.", trimmed),
			Suggestion:    "Ethereum Keccak-256 is outside the fixed image (do not substitute FIPS SHA3-256 for Ethereum Keccak); run in the declared project runner.",
			ExternalRoute: ExternalPythonRoute,
		}
	}
	return Diagnostic{
		Kind:          DiagnosticMissingDistribution,
		Requested:     trimmed,
		Message:       fmt.Sprintf("Package or module %q is not installed in the fixed Portable Python image.", trimmed),
		Suggestion:    "Run `python -m pip list` to inspect installed packages.",
		ExternalRoute: ExternalPythonRoute,
	}
}

// DiagnoseExtraOrBackend reports an unavailable parser engine, backend, or optional extra
// and names the shipped backend/features when the distribution is installed.
func DiagnoseExtraOrBackend(manifest ImageManifest, distributionName, requestedExtra string) Diagnostic {
	req := strings.TrimSpace(requestedExtra)
	if dist, ok := manifest.FindDistribution(distributionName); ok {
		var avail []string
		if dist.Backend != "" {
			avail = append(avail, dist.Backend)
		}
		for _, f := range dist.Features {
			if f != dist.Backend {
				avail = append(avail, f)
			}
		}
		suggestion := fmt.Sprintf("Use the shipped %s backend.", dist.Backend)
		if dist.NormalizedName == "beautifulsoup4" {
			suggestion = "Use the shipped `html.parser` engine with BeautifulSoup(markup, 'html.parser')."
		}
		return Diagnostic{
			Kind:          DiagnosticUnavailableExtra,
			Requested:     req,
			Message:       fmt.Sprintf("Optional extra or backend %q is unavailable for %s %s in the Portable Python image.", req, dist.Name, dist.Version),
			Available:     avail,
			Suggestion:    suggestion,
			ExternalRoute: ExternalPythonRoute,
		}
	}
	return Diagnostic{
		Kind:          DiagnosticUnavailableExtra,
		Requested:     req,
		Message:       fmt.Sprintf("Optional extra or backend %q is unavailable in the Portable Python image.", req),
		ExternalRoute: ExternalPythonRoute,
	}
}

// DiagnoseCLI reports an unadvertised console script and lists the exposed launch CLIs.
func DiagnoseCLI(manifest ImageManifest, cliName string) Diagnostic {
	names := manifest.AdvertisedConsoleScripts()
	return Diagnostic{
		Kind:          DiagnosticUnknownCLI,
		Requested:     cliName,
		Message:       fmt.Sprintf("Console script %q is not an advertised Portable Python command.", cliName),
		Available:     names,
		Suggestion:    "Use an advertised console script or `python -m <module>`.",
		ExternalRoute: ExternalPythonRoute,
	}
}

// DiagnoseMutation reports an attempt to mutate the immutable Portable Python image.
func DiagnoseMutation(command string) Diagnostic {
	return Diagnostic{
		Kind:          DiagnosticImageMutation,
		Requested:     command,
		Message:       fmt.Sprintf("%q cannot run because the Portable Python image has a fixed immutable package set; packages cannot be added or removed.", command),
		Suggestion:    "Use the installed launch packages or switch to a project environment.",
		ExternalRoute: ExternalPythonRoute,
	}
}
