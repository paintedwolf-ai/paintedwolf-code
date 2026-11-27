package pythoncatalog

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// PipResult holds the rendered output and exit status for a read-only pip invocation.
type PipResult struct {
	Stdout     string
	Stderr     string
	ExitStatus int
	Diagnostic *Diagnostic
}

// ExecuteReadOnlyPip formats read-only pip inventory forms bound to manifest.
// Supported forms: list, list --format=json (or --format json), show NAME..., freeze.
// Mutation subcommands return a non-zero status and a DiagnosticImageMutation diagnostic.
func ExecuteReadOnlyPip(manifest ImageManifest, args []string) (PipResult, error) {
	if err := manifest.Validate(); err != nil {
		return PipResult{}, err
	}
	if len(args) == 0 {
		return PipResult{
			Stderr:     "Usage: python -m pip <list|show|freeze> [options]\nRead-only inventory for the immutable Portable Python image.\n",
			ExitStatus: 2,
		}, nil
	}
	sub := strings.TrimSpace(args[0])
	rest := args[1:]
	switch sub {
	case "list":
		return executePipList(manifest, rest), nil
	case "show":
		return executePipShow(manifest, rest), nil
	case "freeze":
		return executePipFreeze(manifest, rest), nil
	case "install", "uninstall", "download", "wheel", "upgrade":
		diag := DiagnoseMutation("pip " + sub)
		return PipResult{
			Stderr:     diag.Format() + "\n",
			ExitStatus: 1,
			Diagnostic: &diag,
		}, nil
	default:
		return PipResult{
			Stderr:     fmt.Sprintf("pip subcommand %q is unsupported in the read-only Portable Python image; supported read-only forms are: list, list --format=json, show NAME, freeze.\n", sub),
			ExitStatus: 2,
		}, nil
	}
}

func executePipList(manifest ImageManifest, args []string) PipResult {
	format := "columns"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--format=json":
			format = "json"
		case arg == "--format=columns":
			format = "columns"
		case arg == "--format=freeze":
			format = "freeze"
		case arg == "--format" && i+1 < len(args):
			i++
			format = args[i]
		default:
			return PipResult{
				Stderr:     fmt.Sprintf("pip list: unsupported option %q\n", arg),
				ExitStatus: 2,
			}
		}
	}
	dists := sortedDistributions(manifest.Distributions)
	switch format {
	case "json":
		type item struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		rows := make([]item, 0, len(dists))
		for _, d := range dists {
			rows = append(rows, item{Name: d.Name, Version: d.Version})
		}
		raw, _ := json.Marshal(rows)
		return PipResult{Stdout: string(raw) + "\n", ExitStatus: 0}
	case "freeze":
		return PipResult{Stdout: formatFreezeLines(dists), ExitStatus: 0}
	case "columns":
		nameWidth := len("Package")
		verWidth := len("Version")
		for _, d := range dists {
			if len(d.Name) > nameWidth {
				nameWidth = len(d.Name)
			}
			if len(d.Version) > verWidth {
				verWidth = len(d.Version)
			}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%-*s %s\n", nameWidth, "Package", "Version")
		fmt.Fprintf(&b, "%s %s\n", strings.Repeat("-", nameWidth), strings.Repeat("-", verWidth))
		for _, d := range dists {
			fmt.Fprintf(&b, "%-*s %s\n", nameWidth, d.Name, d.Version)
		}
		return PipResult{Stdout: b.String(), ExitStatus: 0}
	default:
		return PipResult{
			Stderr:     fmt.Sprintf("pip list: unsupported format %q (supported: columns, json, freeze)\n", format),
			ExitStatus: 2,
		}
	}
}

func executePipShow(manifest ImageManifest, args []string) PipResult {
	var names []string
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return PipResult{
				Stderr:     fmt.Sprintf("pip show: unsupported option %q\n", arg),
				ExitStatus: 2,
			}
		}
		names = append(names, arg)
	}
	if len(names) == 0 {
		return PipResult{
			Stderr:     "ERROR: Please provide a package name or names.\n",
			ExitStatus: 1,
		}
	}
	var out strings.Builder
	var missing []string
	foundCount := 0
	for _, name := range names {
		d, ok := manifest.FindDistribution(name)
		if !ok {
			missing = append(missing, name)
			continue
		}
		if foundCount > 0 {
			out.WriteString("---\n")
		}
		foundCount++
		fmt.Fprintf(&out, "Name: %s\n", d.Name)
		fmt.Fprintf(&out, "Version: %s\n", d.Version)
		fmt.Fprintf(&out, "License: %s\n", d.License)
		fmt.Fprintf(&out, "Location: %s\n", manifest.PackagesRoot)
		fmt.Fprintf(&out, "Imports: %s\n", strings.Join(d.Imports, ", "))
		if d.Backend != "" {
			fmt.Fprintf(&out, "Backend: %s\n", d.Backend)
		}
		if len(d.Features) > 0 {
			fmt.Fprintf(&out, "Features: %s\n", strings.Join(d.Features, ", "))
		}
	}
	if len(missing) > 0 {
		var errOut strings.Builder
		for _, m := range missing {
			fmt.Fprintf(&errOut, "WARNING: Package(s) not found: %s\n", m)
		}
		if foundCount == 0 {
			diag := DiagnoseImport(manifest, missing[0])
			return PipResult{
				Stderr:     errOut.String() + diag.Format() + "\n",
				ExitStatus: 1,
				Diagnostic: &diag,
			}
		}
		return PipResult{Stdout: out.String(), Stderr: errOut.String(), ExitStatus: 0}
	}
	return PipResult{Stdout: out.String(), ExitStatus: 0}
}

func executePipFreeze(manifest ImageManifest, args []string) PipResult {
	if len(args) > 0 {
		return PipResult{
			Stderr:     fmt.Sprintf("pip freeze: unsupported option %q\n", args[0]),
			ExitStatus: 2,
		}
	}
	return PipResult{
		Stdout:     formatFreezeLines(sortedDistributions(manifest.Distributions)),
		ExitStatus: 0,
	}
}

func formatFreezeLines(dists []DistributionRecord) string {
	var b strings.Builder
	for _, d := range dists {
		fmt.Fprintf(&b, "%s==%s\n", d.Name, d.Version)
	}
	return b.String()
}

func sortedDistributions(dists []DistributionRecord) []DistributionRecord {
	out := slices.Clone(dists)
	slices.SortFunc(out, func(a, b DistributionRecord) int {
		return strings.Compare(a.NormalizedName, b.NormalizedName)
	})
	return out
}

// RenderPackageHelp renders compact on-demand package discovery and help from manifest.
func RenderPackageHelp(manifest ImageManifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Portable Python %s (%s / %s)\n", manifest.Interpreter.Version, manifest.Interpreter.Name, manifest.Target)
	fmt.Fprintf(&b, "Image: %s\n\n", manifest.ImageID)

	b.WriteString("Public packages (fixed immutable image):\n")
	for _, d := range manifest.PublicDistributions() {
		imports := strings.Join(d.Imports, ", ")
		fmt.Fprintf(&b, "- %s %s (import %s)", d.Name, d.Version, imports)
		if d.Backend != "" {
			fmt.Fprintf(&b, " [backend: %s]", d.Backend)
		}
		if task := TaskSummaryForDistribution(d.NormalizedName); task != "" {
			fmt.Fprintf(&b, " — %s", task)
		} else if len(d.Features) > 0 {
			fmt.Fprintf(&b, " — %s", strings.Join(d.Features, ", "))
		}
		b.WriteByte('\n')
	}

	if len(manifest.BundledFonts) > 0 {
		b.WriteString("\nBundled Unicode fonts (for PDF layout):\n")
		for _, f := range manifest.BundledFonts {
			fmt.Fprintf(&b, "- %s %s: %s (%s)\n", f.Family, f.Style, f.GuestPath, f.License)
		}
	}

	scripts := manifest.AdvertisedConsoleScripts()
	if len(scripts) > 0 {
		b.WriteString("\nManaged console scripts:\n")
		for _, s := range scripts {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}

	if len(manifest.StdlibCapabilities) > 0 {
		b.WriteString("\nStandard-library capabilities:\n")
		fmt.Fprintf(&b, "- %s\n", strings.Join(manifest.StdlibCapabilities, ", "))
	}

	b.WriteString("\nDiscovery and project-environment routes:\n")
	b.WriteString("- Read-only inventory: python -m pip list | list --format=json | show NAME | freeze\n")
	fmt.Fprintf(&b, "- Packages cannot be added to this image; use `%s` or an explicit project interpreter path for system or project environments.\n", ExternalPythonRoute)
	return b.String()
}

// RenderCompactDiscoverySummary renders the default short task-index discovery summary
// highlighting non-obvious offline capabilities without enumerating every package.
// Explicit inventory requests use RenderPackageHelp or `python -m pip list`.
func RenderCompactDiscoverySummary(manifest ImageManifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Portable Python %s (%s / %s) — %d public packages\n", manifest.Interpreter.Version, manifest.Interpreter.Name, manifest.Target, len(manifest.PublicDistributions()))
	b.WriteString("Notable offline capabilities:\n")
	b.WriteString("- Offline Office & PDF creation: Word (.docx), PowerPoint (.pptx), Excel (.xlsx), Unicode PDF reports (fpdf2 + bundled DejaVu fonts), and PDF reading/merging (pypdf).\n")
	b.WriteString("- Standalone SVG charts & images: offline SVG charts (pygal), workbook charts (openpyxl), and PNG/JPEG processing (Pillow).\n")
	b.WriteString("- Legacy XLS & structured data: binary Excel .xls reading (xlrd), Java .properties (jproperties), SPDX licenses (license_expression), npm/Maven/RubyGems version ranges (univers), GraphQL validation (graphql), and CSS parsing (tinycss2).\n")
	b.WriteString("- Calendar files & timezones: ICS files with TZID/recurrence (icalendar), IANA timezones (zoneinfo + tzdata; pytz compat), and cron schedules (croniter).\n")
	b.WriteString("- Text, diffs, tokens & repo helpers: mojibake repair (ftfy), offline HS256 JWT (jwt), gitignore matching (pathspec), graph cycles (networkx), unified diffs (unidiff), SQL splitting (sqlparse), and strict YAML duplicate-key checks (ruamel.yaml).\n")
	b.WriteString("- Saved-evidence troubleshooting: saved command output (jc), offline pcap/pcapng timelines (dpkt), DNS wire/zone decoding (dns), PEM/DER cert inspection (asn1crypto), log pattern comparison (drain3), typed log parsing (parse), and cross-arch ELF inspection (elftools).\n")
	b.WriteString("On-demand queries:\n")
	b.WriteString("- Targeted help: query by task or import (e.g. \"jwt\", \"repair text\", \"read .xls\", \"saved captures\", \"diagnostic output\", \"log comparison\", \"har\", \"rotated logs\", \"elf\").\n")
	b.WriteString("- Full inventory: python -m pip list | list --format=json | show NAME | freeze.\n")
	fmt.Fprintf(&b, "- Project environments: `%s`.\n", ExternalPythonRoute)
	return b.String()
}

// RenderTargetedHelp renders only the installed distributions and curated examples
// matching query (e.g., "jwt", "repair text", "read .xls", "jproperties", "univers",
// "spdx", "graphql", "css"). When query is empty, returns RenderCompactDiscoverySummary.
func RenderTargetedHelp(manifest ImageManifest, query string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return RenderCompactDiscoverySummary(manifest)
	}
	var matchedDists []DistributionRecord
	for _, d := range manifest.PublicDistributions() {
		if distributionMatchesQuery(d, q) {
			matchedDists = append(matchedDists, d)
		}
	}
	examples := RenderPackageExamples(manifest, q)
	hasExamples := !strings.HasPrefix(examples, "No matching on-demand examples")

	if len(matchedDists) == 0 && !hasExamples {
		return fmt.Sprintf("No matching Portable Python package or example for %q. Run `python -m pip list` for the full inventory.\n", strings.TrimSpace(query))
	}

	var b strings.Builder
	if len(matchedDists) > 0 {
		b.WriteString("Matching packages:\n")
		for _, d := range matchedDists {
			fmt.Fprintf(&b, "- %s %s (import %s)", d.Name, d.Version, strings.Join(d.Imports, ", "))
			if task := TaskSummaryForDistribution(d.NormalizedName); task != "" {
				fmt.Fprintf(&b, " — %s", task)
			}
			b.WriteByte('\n')
		}
	}
	if hasExamples {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(examples)
	}
	return b.String()
}

func distributionMatchesQuery(d DistributionRecord, q string) bool {
	if strings.Contains(strings.ToLower(d.Name), q) ||
		strings.Contains(strings.ToLower(d.NormalizedName), q) ||
		strings.Contains(strings.ToLower(TaskSummaryForDistribution(d.NormalizedName)), q) {
		return true
	}
	for _, imp := range d.Imports {
		if strings.Contains(strings.ToLower(imp), q) {
			return true
		}
	}
	for _, feat := range d.Features {
		if strings.Contains(strings.ToLower(feat), q) {
			return true
		}
	}
	return false
}
