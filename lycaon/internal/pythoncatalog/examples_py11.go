package pythoncatalog

// taskSummaryForPY11Distribution returns concise task-oriented discovery phrases
// for the 34 PY11-Pure public distributions.
func taskSummaryForPY11Distribution(normalizedName string) string {
	switch normalizedName {
	case "pathspec":
		return "match repository paths using gitignore rules (GitIgnoreSpec)"
	case "networkx":
		return "analyze graphs, dependency cycles and reverse dependents"
	case "unidiff":
		return "parse unified diffs with old/new hunk line coordinates"
	case "croniter":
		return "compute next scheduled instants from cron expressions"
	case "pyjwt":
		return "encode and verify JWT tokens offline (explicit HS256 algorithm)"
	case "python-dotenv":
		return "parse .env files and streams including multiline values"
	case "ftfy":
		return "repair mojibake and broken Unicode text"
	case "sqlparse":
		return "split and format SQL statements without a database server"
	case "xmltodict":
		return "convert XML documents to and from Python dicts"
	case "jmespath":
		return "query and project nested JSON structures"
	case "toml":
		return "parse and dump TOML (compatibility import)"
	case "tomli":
		return "parse TOML (stdlib tomllib compatibility import)"
	case "pytest-mock":
		return "mock and spy calls in pytest via mocker fixture"
	case "freezegun":
		return "freeze and restore datetime inside tests"
	case "faker":
		return "generate repeatable seeded synthetic test data"
	case "markdown":
		return "render Markdown to HTML"
	case "markdownify":
		return "convert HTML fragments to Markdown"
	case "html5lib":
		return "parse malformed HTML5 (and BeautifulSoup html5lib builder)"
	case "xlrd":
		return "read legacy binary Excel (.xls) spreadsheets"
	case "filetype":
		return "identify file formats by magic bytes regardless of extension"
	case "pytz":
		return "legacy Olson timezone compatibility (prefer stdlib zoneinfo)"
	case "icalendar":
		return "parse and write ICS calendar files with TZID and recurrence"
	case "tabulate":
		return "format tables as Markdown, grid or plain text"
	case "pygal":
		return "generate standalone offline SVG charts"
	case "tqdm":
		return "wrap iterables with progress helper (use disable=True noninteractively)"
	case "colorama":
		return "format ANSI terminal colors"
	case "termcolor":
		return "format colored text strings"
	case "rich":
		return "render rich tables and formatted output to StringIO"
	case "click":
		return "define and test CLI commands with CliRunner"
	case "univers":
		return "compare versions and test npm, Maven and RubyGems native dependency ranges"
	case "jproperties":
		return "read Java .properties files with escapes, continuations and encoding control"
	case "license-expression":
		return "parse and validate compound SPDX license expressions (AND/OR/WITH)"
	case "graphql-core":
		return "parse and validate GraphQL operations against a local schema"
	case "tinycss2":
		return "parse CSS stylesheets, at-rules and custom properties"
	default:
		return ""
	}
}

// py11OnDemandExamples returns curated on-demand usage snippets for PY11-Pure
// packages and the strict ruamel.yaml duplicate-key workflow.
func py11OnDemandExamples() []CapabilityExample {
	return []CapabilityExample{
		{
			ID:           "ruamel-duplicate-yaml",
			Title:        "Detect duplicate YAML keys with line numbers (strict ruamel.yaml)",
			Distribution: "ruamel-yaml",
			TaskPhrases:  []string{"duplicate YAML keys", "lint YAML duplicates", "ruamel.yaml", "yaml line numbers"},
			Snippet:      "from ruamel.yaml import YAML\nfrom ruamel.yaml.constructor import DuplicateKeyError\ny = YAML(typ='safe'); y.allow_duplicate_keys = False\ntry:\n    y.load(yaml_text)\nexcept DuplicateKeyError as exc:\n    line = exc.problem_mark.line + 1",
		},
		{
			ID:           "pathspec",
			Title:        "Match repository paths with GitIgnoreSpec",
			Distribution: "pathspec",
			TaskPhrases:  []string{"gitignore", "match paths", "pathspec", "GitIgnoreSpec"},
			Snippet:      "from pathspec import GitIgnoreSpec\nspec = GitIgnoreSpec.from_lines(['*.log', '/build/', '!important.log'])\nignored = [p for p in paths if spec.match_file(p)]",
		},
		{
			ID:           "networkx",
			Title:        "Find dependency cycles and reverse dependents with NetworkX",
			Distribution: "networkx",
			TaskPhrases:  []string{"graph cycle", "dependency graph", "reverse dependents", "networkx"},
			Snippet:      "import networkx as nx\ng = nx.DiGraph([('a', 'b'), ('b', 'c'), ('c', 'a'), ('d', 'b')])\ncycles = list(nx.simple_cycles(g))\nrev_deps = nx.ancestors(g, 'b')",
		},
		{
			ID:           "unidiff",
			Title:        "Parse unified diff hunk coordinates with unidiff",
			Distribution: "unidiff",
			TaskPhrases:  []string{"parse unified diff", "patch coordinates", "unidiff"},
			Snippet:      "from unidiff import PatchSet\npatch = PatchSet.from_string(diff_text)\nfor f in patch:\n    for h in f:\n        coords = (h.source_start, h.source_length, h.target_start, h.target_length)",
		},
		{
			ID:           "croniter",
			Title:        "Compute next scheduled instants from a cron expression",
			Distribution: "croniter",
			TaskPhrases:  []string{"cron schedule", "next cron runs", "croniter"},
			Snippet:      "from datetime import datetime, timezone\nfrom croniter import croniter\nbase = datetime(2026, 1, 1, 0, 0, tzinfo=timezone.utc)\nit = croniter('*/15 * * * *', base)\nnext_three = [it.get_next(datetime) for _ in range(3)]",
		},
		{
			ID:           "pyjwt",
			Title:        "Encode and verify JWT tokens offline with explicit algorithm",
			Distribution: "pyjwt",
			TaskPhrases:  []string{"jwt", "verify jwt", "hs256 token", "PyJWT"},
			Snippet:      "import jwt\ntoken = jwt.encode({'sub': 'u1'}, secret, algorithm='HS256')\nclaims = jwt.decode(token, secret, algorithms=['HS256'])",
		},
		{
			ID:           "python-dotenv",
			Title:        "Parse multiline .env content from a stream",
			Distribution: "python-dotenv",
			TaskPhrases:  []string{"dotenv", "parse .env", "env file", "python-dotenv"},
			Snippet:      "import io\nfrom dotenv import dotenv_values\nvalues = dotenv_values(stream=io.StringIO('KEY=value\\nMULTI=\"line1\\nline2\"\\n'))",
		},
		{
			ID:           "ftfy",
			Title:        "Repair mojibake and broken Unicode text with ftfy",
			Distribution: "ftfy",
			TaskPhrases:  []string{"repair text", "fix mojibake", "broken unicode", "ftfy"},
			Snippet:      "import ftfy\nrepaired = ftfy.fix_text('âœ” cafÃ©')",
		},
		{
			ID:           "sqlparse",
			Title:        "Split and format SQL statements with sqlparse",
			Distribution: "sqlparse",
			TaskPhrases:  []string{"split sql", "format sql", "sqlparse"},
			Snippet:      "import sqlparse\nstmts = sqlparse.split(\"SELECT ';'; SELECT 2;\")",
		},
		{
			ID:           "xmltodict",
			Title:        "Parse XML attributes and repeated elements into dicts",
			Distribution: "xmltodict",
			TaskPhrases:  []string{"xml to dict", "xml attributes", "xmltodict"},
			Snippet:      "import xmltodict\ndata = xmltodict.parse('<root><item id=\"1\">a</item><item id=\"2\">b</item></root>')",
		},
		{
			ID:           "jmespath",
			Title:        "Project nested JSON structures with JMESPath",
			Distribution: "jmespath",
			TaskPhrases:  []string{"query json", "project json", "jmespath"},
			Snippet:      "import jmespath\nnames = jmespath.search('items[?active].name', payload)",
		},
		{
			ID:           "xlrd",
			Title:        "Read legacy binary Excel (.xls) workbooks with xlrd",
			Distribution: "xlrd",
			TaskPhrases:  []string{"read .xls", "legacy excel", "binary xls", "xlrd"},
			Snippet:      "import xlrd\nwb = xlrd.open_workbook('legacy.xls')\nsheet = wb.sheet_by_index(0)\nrows = [sheet.row_values(r) for r in range(sheet.nrows)]",
		},
		{
			ID:           "filetype",
			Title:        "Identify file type by magic bytes regardless of suffix",
			Distribution: "filetype",
			TaskPhrases:  []string{"identify file bytes", "magic bytes", "mime type", "filetype"},
			Snippet:      "import filetype\nkind = filetype.guess(raw_bytes)  # kind.mime, kind.extension",
		},
		{
			ID:           "icalendar",
			Title:        "Parse and write ICS calendar files with TZID and recurrence",
			Distribution: "icalendar",
			TaskPhrases:  []string{"calendar files", "ics", "icalendar", "rrule"},
			Snippet:      "from datetime import datetime\nfrom zoneinfo import ZoneInfo\nfrom icalendar import Calendar, Event\ncal = Calendar(); ev = Event()\nev.add('summary', 'Review')\nev.add('dtstart', datetime(2026, 3, 10, 9, 0, tzinfo=ZoneInfo('America/New_York')))\nev.add('rrule', {'freq': 'weekly', 'count': 3})\ncal.add_component(ev)\nics_bytes = cal.to_ical()",
		},
		{
			ID:           "pygal",
			Title:        "Generate a standalone offline SVG chart with pygal",
			Distribution: "pygal",
			TaskPhrases:  []string{"svg chart", "standalone svg", "chart", "pygal"},
			Snippet:      "import pygal\nchart = pygal.Bar(js=[])  # standalone SVG with no external JS\nchart.title = 'Quarterly Revenue'\nchart.x_labels = ['Q1', 'Q2']\nchart.add('North', [10, 14])\nsvg_text = chart.render(is_unicode=True)",
		},
		{
			ID:           "tabulate",
			Title:        "Render Markdown or grid tables with tabulate",
			Distribution: "tabulate",
			TaskPhrases:  []string{"markdown table", "format table", "tabulate"},
			Snippet:      "from tabulate import tabulate\nmd = tabulate([['a', 1], ['b', 2]], headers=['k', 'v'], tablefmt='github')",
		},
		{
			ID:           "univers",
			Title:        "Test npm, Maven and RubyGems native dependency ranges with univers",
			Distribution: "univers",
			TaskPhrases:  []string{"univers", "npm version range", "maven range", "rubygems range", "vers"},
			Snippet:      "from univers.version_range import NpmVersionRange, MavenVersionRange, GemVersionRange\nfrom univers.versions import SemverVersion, MavenVersion, RubygemsVersion\n# Native manifest ranges (distinct from normalized 'vers:...' syntax):\nassert SemverVersion('1.2.5') in NpmVersionRange.from_native('^1.2.0')\nassert MavenVersion('2.0') in MavenVersionRange.from_native('[1.0,2.0]')\nassert RubygemsVersion('1.4.9') in GemVersionRange.from_native('~> 1.4.2')",
		},
		{
			ID:           "jproperties",
			Title:        "Read Java .properties files with explicit encoding (ISO-8859-1 or UTF-8)",
			Distribution: "jproperties",
			TaskPhrases:  []string{"jproperties", "java properties", "properties encoding"},
			Snippet:      "from jproperties import Properties\np = Properties()\n# Java Properties.load(InputStream) uses ISO-8859-1 by default (use encoding='utf-8' for Java 9+ UTF-8 files)\nwith open('app.properties', 'rb') as fh:\n    p.load(fh, encoding='iso-8859-1')\nval = p['db.url'].data",
		},
		{
			ID:           "license-expression",
			Title:        "Parse and validate compound SPDX license expressions",
			Distribution: "license-expression",
			TaskPhrases:  []string{"spdx", "license expression", "license_expression", "and or with"},
			Snippet:      "from license_expression import get_spdx_licensing\nlicensing = get_spdx_licensing()\nparsed = licensing.parse('(MIT OR Apache-2.0) AND GPL-2.0-only WITH Classpath-exception-2.0', validate=True)",
		},
		{
			ID:           "graphql-core",
			Title:        "Parse and validate a GraphQL operation against a local schema",
			Distribution: "graphql-core",
			TaskPhrases:  []string{"graphql", "validate graphql", "graphql schema", "graphql-core"},
			Snippet:      "from graphql import build_schema, parse, validate\nschema = build_schema('type Query { user(id: ID!): String }')\nerrors = validate(schema, parse('query($id: ID!) { user(id: $id) }'))",
		},
		{
			ID:           "tinycss2",
			Title:        "Parse CSS stylesheets, at-rules and custom properties with tinycss2",
			Distribution: "tinycss2",
			TaskPhrases:  []string{"css", "parse css", "stylesheet", "tinycss2"},
			Snippet:      "import tinycss2\nrules = tinycss2.parse_stylesheet('@media screen { :root { --x: \"a;b\"; } }', skip_whitespace=True)",
		},
	}
}
