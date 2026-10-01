package logview

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseLogLine(t *testing.T) {
	r := parseLogLine(`time=2026-06-22T13:31:11.499-07:00 level=WARN msg="something bad" path=/x count=3`)
	if r.Level != "WARN" {
		t.Errorf("level = %q", r.Level)
	}
	if r.Msg != "something bad" {
		t.Errorf("msg = %q", r.Msg)
	}
	if !strings.Contains(r.Attrs, "path=/x") || !strings.Contains(r.Attrs, "count=3") {
		t.Errorf("attrs = %q", r.Attrs)
	}
	if r.Time.IsZero() {
		t.Error("time should parse")
	}
}

func TestFilterLogsByLevel(t *testing.T) {
	recs := []LogRecord{
		{Level: "DEBUG", Msg: "d"},
		{Level: "INFO", Msg: "i"},
		{Level: "WARN", Msg: "w"},
		{Level: "ERROR", Msg: "e"},
	}
	if got := CountLogsAtLeast(recs, warnRankTest); got != 2 {
		t.Errorf("warn+error count = %d, want 2", got)
	}
	if got := len(FilterLogs(recs, warnRankTest)); got != 2 {
		t.Errorf("filtered = %d, want 2", got)
	}
}

const warnRankTest = 2

func TestLogDetailIncludesAttrs(t *testing.T) {
	var buf bytes.Buffer
	testDisplay().LogDetail(&buf, LogRecord{Level: "ERROR", Msg: "boom", Attrs: "code=5 path=/a"})
	out := buf.String()
	if !strings.Contains(out, "ERROR") || !strings.Contains(out, "boom") || !strings.Contains(out, "code=5") {
		t.Errorf("log detail = %q", out)
	}
}
