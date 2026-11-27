package pythoncatalog

import (
	"fmt"
	"slices"
	"strings"
)

// CapabilityExample holds a compact on-demand usage snippet for an installed
// distribution or standard-library capability. Examples are rendered only on
// demand (never injected into standing system prompts).
type CapabilityExample struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Distribution string   `json:"distribution,omitempty"`
	StdlibModule string   `json:"stdlib_module,omitempty"`
	TaskPhrases  []string `json:"task_phrases"`
	Snippet      string   `json:"snippet"`
}

// TaskSummaryForDistribution returns a concise task-oriented discovery phrase
// for a normalized distribution name, or "" if none is registered.
func TaskSummaryForDistribution(normalizedName string) string {
	switch normalizedName {
	case "pyyaml":
		return "read and write YAML configs"
	case "openpyxl":
		return "read, edit and chart Excel workbooks (.xlsx)"
	case "pytest":
		return "run Python tests"
	case "tomlkit":
		return "edit TOML files preserving formatting"
	case "ruamel-yaml":
		return "round-trip YAML preserving comments and anchors"
	case "packaging":
		return "parse PEP 440 versions, specifiers and markers"
	case "jsonschema":
		return "validate JSON against Draft 2020-12 schemas"
	case "asttokens":
		return "map Python AST nodes to source token ranges"
	case "jinja2":
		return "render Jinja2 templates"
	case "beautifulsoup4":
		return "parse HTML and query with CSS selectors"
	case "markdown-it-py":
		return "parse CommonMark Markdown with token line maps"
	case "requests":
		return "make host-mediated synchronous HTTPS requests"
	case "pypdf":
		return "extract text, inspect metadata and merge PDFs"
	case "json5":
		return "parse JSON5 configs with comments and trailing commas"
	case "tzdata":
		return "convert timezones using named IANA zones (zoneinfo)"
	case "python-dateutil":
		return "calculate calendar recurrences and reporting periods"
	case "sympy":
		return "solve and verify exact symbolic math"
	case "pillow":
		return "inspect, resize and convert PNG/JPEG images"
	case "lxml":
		return "query XML with XPath and namespaces"
	case "python-docx":
		return "read or write a Word document (.docx)"
	case "python-pptx":
		return "read and write a slide deck (.pptx)"
	case "fpdf2":
		return "lay out a PDF report with Unicode text"
	case "pygments":
		return "tokenize source code across languages"
	case "charset-normalizer":
		return "detect legacy text encoding candidates"
	default:
		return taskSummaryForPY11Distribution(normalizedName)
	}
}

// OnDemandExamples returns the curated on-demand examples for PY10/PY11 capabilities
// and baseline discoveries (Pygments, charset-normalizer, sqlite3, openpyxl charts).
func OnDemandExamples() []CapabilityExample {
	base := []CapabilityExample{
		{
			ID:           "json5",
			Title:        "Parse JSON5 config with comments and trailing commas",
			Distribution: "json5",
			TaskPhrases:  []string{"parse JSON5", "read config with comments and trailing commas", "json5"},
			Snippet:      "import json5\ndata = json5.loads('// comment\\n{\"url\": \"https://example.com\", \"items\": [1, 2,],}')",
		},
		{
			ID:           "tzdata-zoneinfo",
			Title:        "Convert dates using named IANA timezones (tzdata + zoneinfo)",
			Distribution: "tzdata",
			TaskPhrases:  []string{"convert timezones", "named IANA zones", "zoneinfo", "tzdata"},
			Snippet:      "from datetime import datetime\nfrom zoneinfo import ZoneInfo\ndt = datetime(2026, 11, 1, 1, 30, fold=1, tzinfo=ZoneInfo('America/New_York'))\nktm = dt.astimezone(ZoneInfo('Asia/Kathmandu'))",
		},
		{
			ID:           "python-dateutil",
			Title:        "Calculate calendar recurrences and reporting periods",
			Distribution: "python-dateutil",
			TaskPhrases:  []string{"calendar recurrences", "reporting periods", "dateutil", "rrule", "relativedelta"},
			Snippet:      "from datetime import datetime\nfrom dateutil.rrule import rrule, MONTHLY, TU\nfrom dateutil.relativedelta import relativedelta\nthirs = list(rrule(MONTHLY, byweekday=TU(3), count=4, dtstart=datetime(2026, 1, 1)))",
		},
		{
			ID:           "sympy",
			Title:        "Solve and verify exact symbolic math (pure-Python + mpmath)",
			Distribution: "sympy",
			TaskPhrases:  []string{"exact symbolic math", "solve equations", "sympy", "diff"},
			Snippet:      "import sympy as sp\nx, y = sp.symbols('x y')\nsol = sp.solve([sp.Eq(2*x + y, 5), sp.Eq(x - y, 1)], (x, y), dict=True)\nexpr = x**3 - 3*x\nassert sp.diff(expr, x).subs(x, sp.Rational(2, 1)) == 9",
		},
		{
			ID:           "pillow",
			Title:        "Inspect, EXIF-transpose, resize and convert PNG/JPEG images",
			Distribution: "pillow",
			TaskPhrases:  []string{"resize an image", "convert image", "exif orientation", "pillow", "PIL"},
			Snippet:      "from PIL import Image, ImageOps\nwith Image.open('photo.jpg') as im:\n    out = ImageOps.exif_transpose(im).resize((320, 240), Image.Resampling.LANCZOS)\n    out.save('thumb.png', format='PNG')",
		},
		{
			ID:           "lxml",
			Title:        "Query namespaced XML using XPath (local parser, no external fetch)",
			Distribution: "lxml",
			TaskPhrases:  []string{"query XML with XPath", "namespaced XML", "lxml", "lxml.etree"},
			Snippet:      "from lxml import etree\nparser = etree.XMLParser(resolve_entities=False, no_network=True)\nroot = etree.fromstring(xml_bytes, parser=parser)\nnodes = root.xpath('//ns:item/text()', namespaces={'ns': 'urn:example'})",
		},
		{
			ID:           "python-docx",
			Title:        "Read, edit and write a Word document (.docx)",
			Distribution: "python-docx",
			TaskPhrases:  []string{"read or write a Word document", "edit docx", "python-docx", "docx"},
			Snippet:      "from docx import Document\nfrom docx.shared import Inches\ndoc = Document('input.docx')\ndoc.tables[0].cell(1, 1).text = 'Updated'\ndoc.add_paragraph('Summary')\ndoc.add_picture('chart.png', width=Inches(3.0))\ndoc.save('output.docx')",
		},
		{
			ID:           "python-pptx",
			Title:        "Create a slide deck (.pptx) with text, image and chart",
			Distribution: "python-pptx",
			TaskPhrases:  []string{"write a slide deck", "create presentation", "python-pptx", "pptx"},
			Snippet:      "from pptx import Presentation\nfrom pptx.chart.data import CategoryChartData\nfrom pptx.enum.chart import XL_CHART_TYPE\nfrom pptx.util import Inches\nprs = Presentation()\nslide = prs.slides.add_slide(prs.slide_layouts[5])\ndata = CategoryChartData(); data.categories = ['Q1', 'Q2']; data.add_series('Rev', (10, 14))\nslide.shapes.add_chart(XL_CHART_TYPE.COLUMN_CLUSTERED, Inches(1), Inches(1.5), Inches(6), Inches(4), data)\nprs.save('deck.pptx')",
		},
		{
			ID:           "fpdf2",
			Title:        "Lay out a Unicode PDF report with bundled DejaVuSans font",
			Distribution: "fpdf2",
			TaskPhrases:  []string{"lay out a PDF report", "generate PDF", "fpdf2", "fpdf"},
			Snippet:      "from fpdf import FPDF\npdf = FPDF()\npdf.add_font('DejaVu', '', '/packages/fonts/DejaVuSans.ttf')\npdf.add_font('DejaVu', 'B', '/packages/fonts/DejaVuSans-Bold.ttf')\npdf.add_page()\npdf.set_font('DejaVu', 'B', 14)\npdf.cell(0, 10, 'Quarterly Report — Zurich & Tokyo', new_x='LMARGIN', new_y='NEXT')\npdf.output('report.pdf')",
		},
		{
			ID:           "pygments",
			Title:        "Tokenize source code with Pygments",
			Distribution: "pygments",
			TaskPhrases:  []string{"tokenize source code", "syntax highlight", "pygments"},
			Snippet:      "from pygments import lex\nfrom pygments.lexers import get_lexer_by_name\ntokens = list(lex('def f(x): return x + 1\\n', get_lexer_by_name('python')))",
		},
		{
			ID:           "charset-normalizer",
			Title:        "Inspect legacy text encoding candidates with charset-normalizer",
			Distribution: "charset-normalizer",
			TaskPhrases:  []string{"detect text encoding", "legacy encoding", "charset-normalizer", "charset_normalizer"},
			Snippet:      "from charset_normalizer import from_bytes\nresults = from_bytes(raw_bytes)\nbest = results.best()\nencoding, decoded = (best.encoding, str(best)) if best else (None, '')",
		},
		{
			ID:           "sqlite3-join",
			Title:        "Join CSV and JSONL records in an in-memory sqlite3 database",
			StdlibModule: "sqlite3",
			TaskPhrases:  []string{"join CSV and JSONL", "in-memory SQL", "sqlite3", "tabular analysis"},
			Snippet:      "import csv, json, sqlite3\ncon = sqlite3.connect(':memory:')\ncon.execute('CREATE TABLE users(id TEXT, name TEXT)')\ncon.execute('CREATE TABLE events(user_id TEXT, amount REAL)')\n# Insert from csv.DictReader / json.loads lines, then SELECT u.name, SUM(e.amount) FROM users u JOIN events e ON u.id = e.user_id GROUP BY u.name",
		},
		{
			ID:           "openpyxl-chart",
			Title:        "Create a data-to-workbook chart with openpyxl",
			Distribution: "openpyxl",
			TaskPhrases:  []string{"excel chart", "workbook chart", "openpyxl chart", "openpyxl"},
			Snippet:      "from openpyxl import Workbook\nfrom openpyxl.chart import BarChart, Reference\nwb = Workbook(); ws = wb.active\nfor row in [('Month', 'Sales'), ('Jan', 10), ('Feb', 15)]: ws.append(row)\nchart = BarChart()\nchart.add_data(Reference(ws, min_col=2, min_row=1, max_row=3), titles_from_data=True)\nchart.set_categories(Reference(ws, min_col=1, min_row=2, max_row=3))\nws.add_chart(chart, 'D2'); wb.save('chart.xlsx')",
		},
	}
	return append(base, py11OnDemandExamples()...)
}

// RenderPackageExamples renders concise on-demand usage examples filtered to
// capabilities actually installed in manifest. When query is non-empty, only
// examples matching query (by ID, distribution, stdlib module, title, or task
// phrase) are included.
func RenderPackageExamples(manifest ImageManifest, query string) string {
	q := strings.ToLower(strings.TrimSpace(query))
	var b strings.Builder
	matched := 0
	for _, ex := range OnDemandExamples() {
		if !exampleAvailableInManifest(manifest, ex) {
			continue
		}
		if q != "" && !exampleMatchesQuery(ex, q) {
			continue
		}
		if matched > 0 {
			b.WriteByte('\n')
		}
		matched++
		fmt.Fprintf(&b, "### %s (%s)\n", ex.Title, ex.ID)
		fmt.Fprintf(&b, "Tasks: %s\n", strings.Join(ex.TaskPhrases, "; "))
		fmt.Fprintf(&b, "```python\n%s\n```\n", ex.Snippet)
	}
	if matched == 0 {
		return "No matching on-demand examples found for the active Portable Python image.\n"
	}
	return b.String()
}

func exampleAvailableInManifest(manifest ImageManifest, ex CapabilityExample) bool {
	if ex.Distribution != "" {
		_, ok := manifest.FindDistribution(ex.Distribution)
		return ok
	}
	if ex.StdlibModule != "" {
		return slices.Contains(manifest.StdlibCapabilities, ex.StdlibModule)
	}
	return false
}

func exampleMatchesQuery(ex CapabilityExample, q string) bool {
	if strings.Contains(strings.ToLower(ex.ID), q) ||
		strings.Contains(strings.ToLower(ex.Title), q) ||
		strings.Contains(strings.ToLower(ex.Distribution), q) ||
		strings.Contains(strings.ToLower(ex.StdlibModule), q) {
		return true
	}
	for _, phrase := range ex.TaskPhrases {
		if strings.Contains(strings.ToLower(phrase), q) {
			return true
		}
	}
	return false
}
