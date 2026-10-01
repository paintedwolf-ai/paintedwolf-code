package output_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const semgrepLiteMapper = `id: semgrep_sarif_lite
stdout_format: json
items_path: /results
fields:
  rule_id: /check_id
  level: /extra/severity
  message: /extra/message
  uri: /path
  start_line: /start/line
level_map:
  ERROR: high
  WARNING: medium
  INFO: low
`

func writeMapper(t *testing.T, configDir, mapperID, body string) {
	t.Helper()
	dir := filepath.Join(configDir, "scanners", "mappers")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir mappers", err)
	}
	if err := os.WriteFile(filepath.Join(dir, mapperID+".yaml"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write mapper", err)
	}
}

func TestLoadMapperGolden(t *testing.T) {
	configDir := t.TempDir()
	writeMapper(t, configDir, "semgrep_sarif_lite", semgrepLiteMapper)
	m, err := scanoutput.LoadMapper(configDir, "semgrep_sarif_lite")
	testutil.FailErr(t, "scan.LoadMapper failed", err)
	if m.ItemsPath != "/results" || m.Fields.RuleID != "/check_id" {
		t.Fatalf("mapper = %+v", m)
	}
	if m.LevelMap["ERROR"] != "high" {
		t.Fatalf("level_map = %v", m.LevelMap)
	}
}

func TestLoadMapperRejectsBadIDs(t *testing.T) {
	configDir := t.TempDir()
	for _, id := range []string{"", "UPPER", "1leading", "has/slash", "has..dots", "../escape"} {
		if _, err := scanoutput.LoadMapper(configDir, id); err == nil {
			t.Fatalf("id %q: expected error", id)
		}
	}
}

func TestLoadMapperMissingFile(t *testing.T) {
	_, err := scanoutput.LoadMapper(t.TempDir(), "nope")
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadMapperValidation(t *testing.T) {
	configDir := t.TempDir()
	for name, body := range map[string]string{
		"id_mismatch":     "id: other\nstdout_format: json\nitems_path: /r\nfields:\n  rule_id: /a\n  level: /b\n",
		"bad_format":      "id: bad\nstdout_format: xml\nitems_path: /r\nfields:\n  rule_id: /a\n  level: /b\n",
		"missing_items":   "id: bad\nstdout_format: json\nfields:\n  rule_id: /a\n  level: /b\n",
		"missing_rule_id": "id: bad\nstdout_format: json\nitems_path: /r\nfields:\n  level: /b\n",
		"missing_level":   "id: bad\nstdout_format: json\nitems_path: /r\nfields:\n  rule_id: /a\n",
		"bad_level_map":   "id: bad\nstdout_format: json\nitems_path: /r\nfields:\n  rule_id: /a\n  level: /b\nlevel_map:\n  X: catastrophic\n",
	} {
		writeMapper(t, configDir, "bad", body)
		if _, err := scanoutput.LoadMapper(configDir, "bad"); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestMapJSONParserGolden(t *testing.T) {
	configDir := t.TempDir()
	writeMapper(t, configDir, "semgrep_sarif_lite", semgrepLiteMapper)
	m, err := scanoutput.LoadMapper(configDir, "semgrep_sarif_lite")
	testutil.FailErr(t, "scan.LoadMapper failed", err)
	p := scanoutput.NewMapJSONParser(m)
	raw := []byte(`{"results":[
		{"check_id":"py.sql-injection","path":"app/db.py","start":{"line":12},"extra":{"severity":"ERROR","message":"tainted SQL"}},
		{"check_id":"py.weak-hash","path":"app/auth.py","start":{"line":3},"extra":{"severity":"INFO","message":"md5 in use"}}
	]}`)
	res, err := p.Parse(raw)
	testutil.FailErr(t, "map/json parse failed", err)
	if res.FindingsCount != 2 {
		t.Fatalf("findings = %d", res.FindingsCount)
	}
	first := res.Findings[0]
	if first.RuleID != "py.sql-injection" || first.Level != api.FindingLevelHigh {
		t.Fatalf("first = %+v", first)
	}
	if first.Message != "tainted SQL" {
		t.Fatalf("message = %q", first.Message)
	}
	if first.Locations[0].URI != "app/db.py" || first.Locations[0].StartLine != 12 {
		t.Fatalf("location = %+v", first.Locations[0])
	}
	// INFO maps to the canonical low level.
	if res.Findings[1].Level != api.FindingLevelLow {
		t.Fatalf("second level = %q", res.Findings[1].Level)
	}
}

func TestMapJSONParserErrors(t *testing.T) {
	configDir := t.TempDir()
	writeMapper(t, configDir, "semgrep_sarif_lite", semgrepLiteMapper)
	m, err := scanoutput.LoadMapper(configDir, "semgrep_sarif_lite")
	testutil.FailErr(t, "scan.LoadMapper failed", err)
	p := scanoutput.NewMapJSONParser(m)
	// Envelope errors are fatal; per-row data is coerced (see coercion test).
	for name, raw := range map[string]string{
		"invalid_json":    `{"results":[`,
		"items_not_found": `{"other":[]}`,
		"items_not_array": `{"results":{}}`,
	} {
		if _, err := p.Parse([]byte(raw)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestMapJSONParserRowCoercion(t *testing.T) {
	configDir := t.TempDir()
	writeMapper(t, configDir, "semgrep_sarif_lite", semgrepLiteMapper)
	m, err := scanoutput.LoadMapper(configDir, "semgrep_sarif_lite")
	testutil.FailErr(t, "scan.LoadMapper failed", err)
	p := scanoutput.NewMapJSONParser(m)

	// A row with no rule id is skipped; a valid row alongside it survives.
	res, err := p.Parse([]byte(`{"results":[
		{"path":"f","extra":{"severity":"ERROR"}},
		{"check_id":"real","path":"g","start":{"line":9},"extra":{"severity":"ERROR"}}
	]}`))
	testutil.FailErr(t, "skip parse", err)
	if res.FindingsCount != 1 || res.Findings[0].RuleID != "real" {
		t.Fatalf("skip: findings = %+v", res.Findings)
	}

	// Unmapped severities stay unknown.
	res, err = p.Parse([]byte(`{"results":[{"check_id":"r","extra":{"severity":"NUCLEAR"}}]}`))
	testutil.FailErr(t, "coerce parse", err)
	if res.FindingsCount != 1 || res.Findings[0].Level != api.FindingLevelUnknown {
		t.Fatalf("coerce: findings = %+v", res.Findings)
	}
}

func TestMapJSONArrayIndexOverflowNoPanic(t *testing.T) {
	configDir := t.TempDir()
	body := `id: overflow_probe
stdout_format: json
items_path: /results
fields:
  rule_id: /check
  level: /sev
  uri: /refs/99999999999999999999
`
	writeMapper(t, configDir, "overflow_probe", body)
	m, err := scanoutput.LoadMapper(configDir, "overflow_probe")
	testutil.FailErr(t, "scan.LoadMapper failed", err)
	p := scanoutput.NewMapJSONParser(m)
	// Oversized array indices resolve to missing values.
	res, err := p.Parse([]byte(`{"results":[{"check":"r","sev":"high","refs":[1,2]}]}`))
	testutil.FailErr(t, "map/json parse failed", err)
	if res.FindingsCount != 1 {
		t.Fatalf("findings = %d", res.FindingsCount)
	}
	if res.Findings[0].Locations[0].URI != "" {
		t.Fatalf("uri = %q, want empty (index out of range)", res.Findings[0].Locations[0].URI)
	}
}

func TestMapJSONPlaceholderRegisteredButUnusable(t *testing.T) {
	if !scanoutput.IsRegisteredOutputParser(scanoutput.OutputParserMapJSON) {
		t.Fatal("map/json not registered")
	}
	_, err := scanoutput.ParseOutput(scanoutput.OutputParserMapJSON, []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "mapper required") {
		t.Fatalf("error = %v", err)
	}
}
