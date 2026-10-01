package logoutline_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/logoutline"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return b
}

func readNegative(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "negative", name))
	if err != nil {
		t.Fatalf("read negative %s: %v", name, err)
	}
	return b
}

func TestClassifyPositiveFormats(t *testing.T) {
	cases := []struct {
		file   string
		format logoutline.LogFormat
	}{
		{"json_lines.log", logoutline.FormatJSONLines},
		{"logfmt.log", logoutline.FormatLogfmt},
		{"syslog_rfc5424.log", logoutline.FormatSyslogRFC5424},
		{"syslog_rfc3164.log", logoutline.FormatSyslogRFC3164},
		{"cef.log", logoutline.FormatCEF},
		{"leef.log", logoutline.FormatLEEF},
		{"clf.log", logoutline.FormatCLF},
		{"combined.log", logoutline.FormatCombined},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			format, conf := logoutline.Classify(readTestdata(t, tc.file))
			if format != tc.format {
				t.Fatalf("Classify = %q want %q (conf=%v)", format, tc.format, conf)
			}
			if conf < 0.85 {
				t.Fatalf("confidence = %v want >= 0.85", conf)
			}
		})
	}
}

func TestClassifyNegativeCorpus(t *testing.T) {
	files := []string{
		"go_source.go",
		"typescript.ts",
		"python.py",
		"config.yaml",
		"config.json",
		"readme.md",
		"prose.txt",
	}
	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			format, conf := logoutline.Classify(readNegative(t, name))
			if format != logoutline.FormatNone {
				t.Fatalf("Classify = %q conf=%v want FormatNone", format, conf)
			}
		})
	}
}

func TestParseJSONLines(t *testing.T) {
	p, ok := logoutline.ParserFor(logoutline.FormatJSONLines)
	if !ok {
		t.Fatal("json_lines parser missing")
	}
	rec, ok := p.Parse([]byte(`{"level":"info","ts":"2024-01-15T10:00:00Z","msg":"started","host":"h1"}`))
	if !ok {
		t.Fatal("parse failed")
	}
	if rec.Severity != "info" || rec.Message != "started" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.Fields["host"] != "h1" {
		t.Fatalf("fields = %+v", rec.Fields)
	}
	if rec.Time.IsZero() {
		t.Fatal("time not parsed")
	}
}

func TestParseLogfmt(t *testing.T) {
	p, _ := logoutline.ParserFor(logoutline.FormatLogfmt)
	rec, ok := p.Parse([]byte(`level=error ts=2024-01-15T10:01:00Z msg="connection refused" code=503`))
	if !ok {
		t.Fatal("parse failed")
	}
	if rec.Severity != "error" || rec.Message != "connection refused" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.Fields["code"] != "503" {
		t.Fatalf("fields = %+v", rec.Fields)
	}
}

func TestParseSyslogRFC5424(t *testing.T) {
	p, _ := logoutline.ParserFor(logoutline.FormatSyslogRFC5424)
	line := []byte(`<34>1 2024-01-15T10:00:00.003Z host1 myapp 1234 ID47 - Application started`)
	rec, ok := p.Parse(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if rec.Severity != "crit" {
		t.Fatalf("severity = %q want crit", rec.Severity)
	}
	if rec.Fields["host"] != "host1" || rec.Fields["app"] != "myapp" {
		t.Fatalf("fields = %+v", rec.Fields)
	}
	if rec.Message != "- Application started" {
		t.Fatalf("message = %q", rec.Message)
	}
	want, _ := time.Parse(time.RFC3339Nano, "2024-01-15T10:00:00.003Z")
	if !rec.Time.Equal(want) {
		t.Fatalf("time = %v want %v", rec.Time, want)
	}
}

func TestParseSyslogRFC3164(t *testing.T) {
	p, _ := logoutline.ParserFor(logoutline.FormatSyslogRFC3164)
	line := []byte(`<34>Jan 15 10:00:00 host1 su: session opened for user root`)
	rec, ok := p.Parse(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if rec.Severity != "crit" {
		t.Fatalf("severity = %q", rec.Severity)
	}
	if rec.Fields["host"] != "host1" || rec.Fields["tag"] != "su" {
		t.Fatalf("fields = %+v", rec.Fields)
	}
	if rec.Message != "session opened for user root" {
		t.Fatalf("message = %q", rec.Message)
	}
}

func TestParseCEF(t *testing.T) {
	p, _ := logoutline.ParserFor(logoutline.FormatCEF)
	line := []byte(`CEF:0|Vendor|Product|1.0|100|Login failed|7|src=10.0.0.1 msg=auth failure rt=Jan 15 2024 10:00:00`)
	rec, ok := p.Parse(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if rec.Severity != "7" || rec.Fields["vendor"] != "Vendor" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.Fields["src"] != "10.0.0.1" {
		t.Fatalf("fields = %+v", rec.Fields)
	}
}

func TestParseLEEF(t *testing.T) {
	p, _ := logoutline.ParserFor(logoutline.FormatLEEF)
	line := []byte("LEEF:1.0|Vendor|Product|1.0|200|devTime=2024-01-15T10:00:00Z\tsev=5\tcat=auth\tsrc=10.0.0.1")
	rec, ok := p.Parse(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if rec.Severity != "5" || rec.Fields["cat"] != "auth" {
		t.Fatalf("rec = %+v", rec)
	}
	if rec.Message != "auth" {
		t.Fatalf("message = %q", rec.Message)
	}
}

func TestParseCLF(t *testing.T) {
	p, _ := logoutline.ParserFor(logoutline.FormatCLF)
	line := []byte(`127.0.0.1 - - [15/Jan/2024:10:00:00 +0000] "GET /index.html HTTP/1.1" 200 512`)
	rec, ok := p.Parse(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if rec.Fields["status"] != "200" || rec.Fields["bytes"] != "512" {
		t.Fatalf("fields = %+v", rec)
	}
	if rec.Message != "GET /index.html HTTP/1.1" {
		t.Fatalf("message = %q", rec.Message)
	}
}

func TestParseCombined(t *testing.T) {
	p, _ := logoutline.ParserFor(logoutline.FormatCombined)
	line := []byte(`127.0.0.1 - - [15/Jan/2024:10:00:00 +0000] "GET / HTTP/1.1" 200 512 "http://example.com/" "Mozilla/5.0"`)
	rec, ok := p.Parse(line)
	if !ok {
		t.Fatal("parse failed")
	}
	if rec.Fields["referer"] != "http://example.com/" {
		t.Fatalf("referer = %q", rec.Fields["referer"])
	}
	if rec.Fields["user_agent"] != "Mozilla/5.0" {
		t.Fatalf("user_agent = %q", rec.Fields["user_agent"])
	}
}

func TestClassifyUsesHeadSampleOnly(t *testing.T) {
	head := readTestdata(t, "json_lines.log")
	var padded []byte
	for len(padded) < 8192 {
		padded = append(padded, head...)
	}
	padded = append(padded, []byte("<<<<not valid json\n")...)
	format, conf := logoutline.Classify(padded)
	if format != logoutline.FormatJSONLines || conf < 0.85 {
		t.Fatalf("Classify = %q %v on byte-capped sample", format, conf)
	}
}

const throwawayFormat logoutline.LogFormat = "throwaway_test_format"

type throwawayParser struct{}

func (throwawayParser) Format() logoutline.LogFormat { return throwawayFormat }

func (throwawayParser) Parse(line []byte) (logoutline.Record, bool) {
	return logoutline.Record{Message: string(line)}, true
}

func TestRegistryPluggableFormat(t *testing.T) {
	logoutline.RegisterParser(throwawayParser{})
	logoutline.RegisterClassifierMatcher(throwawayFormat, 0.5, 200, func(line []byte) bool {
		return len(line) >= 4 && string(line[:4]) == "ZZZZ"
	})
	format, conf := logoutline.Classify([]byte("ZZZZ event\nZZZZ again\n"))
	if format != throwawayFormat || conf != 1 {
		t.Fatalf("Classify = %q %v want throwaway 1", format, conf)
	}
	p, ok := logoutline.ParserFor(throwawayFormat)
	if !ok {
		t.Fatal("ParserFor throwaway missing")
	}
	rec, ok := p.Parse([]byte("hello"))
	if !ok || rec.Message != "hello" {
		t.Fatalf("parse = %+v %v", rec, ok)
	}
}

func TestParserRejectsNonMatchingLine(t *testing.T) {
	p, _ := logoutline.ParserFor(logoutline.FormatCEF)
	if _, ok := p.Parse([]byte("not cef")); ok {
		t.Fatal("expected parse failure")
	}
}

func TestCombinedWinsOverCLF(t *testing.T) {
	format, _ := logoutline.Classify(readTestdata(t, "combined.log"))
	if format != logoutline.FormatCombined {
		t.Fatalf("Classify = %q want combined", format)
	}
}
