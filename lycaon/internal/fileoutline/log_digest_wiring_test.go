package fileoutline_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBuildLogFileReturnsDigest(t *testing.T) {
	dir := t.TempDir()
	line := `{"level":"info","ts":"2024-01-15T10:00:00Z","msg":"started"}` + "\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "app.log"), []byte(line+line+line), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "app.log")
	testutil.FailErr(t, "Build", err)
	if out.Source != "log" {
		t.Fatalf("source = %q want log", out.Source)
	}
	if out.LogDigest == nil || out.LogDigest.Format != logoutline.FormatJSONLines {
		t.Fatalf("log_digest = %+v", out.LogDigest)
	}
	if len(out.Symbols) != 0 {
		t.Fatalf("symbols = %+v want empty", out.Symbols)
	}
	if out.Parses != nil {
		t.Fatalf("parses = %v want omitted for log", out.Parses)
	}
}

func TestBuildSyslogAuthLogDigest(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		`<30>Jan 15 10:00:00 host1 sshd: Failed password for invalid user admin from 203.0.113.7 port 55012`,
		`<30>Jan 15 10:00:01 host1 sshd: Failed password for invalid user root from 198.51.100.9 port 41044`,
		`<30>Jan 15 10:00:02 host1 sshd: Failed password for invalid user admin from 203.0.113.8 port 55013`,
		`<30>Jan 15 10:00:03 host1 sshd: Accepted publickey for deploy from 10.0.0.1 port 22`,
		`<30>Jan 15 10:00:04 host1 sshd: Failed password for invalid user admin from 203.0.113.7 port 55014`,
	}, "\n") + "\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "auth.log"), []byte(body), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "auth.log")
	testutil.FailErr(t, "Build", err)
	if out.LogDigest == nil || out.LogDigest.Format != logoutline.FormatSyslogRFC3164 {
		t.Fatalf("digest = %+v", out.LogDigest)
	}
	if len(out.LogDigest.Clusters) == 0 {
		t.Fatal("expected recurring-event clusters")
	}
}

func TestBuildCodeFilesNoLogDigest(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string
	}{
		{"main.go", "package main\n\nfunc main() {}\n"},
		{"app.ts", "export function greet(): string {\n  return \"hi\";\n}\n"},
		{"game.py", "def update():\n    pass\n"},
		{"config.yaml", "server:\n  port: 8080\nlogging:\n  level: info\n"},
		{"config.json", "{\n  \"server\": {\"port\": 8080},\n  \"logging\": {\"level\": \"info\"}\n}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, tc.name), []byte(tc.body), 0o644))
			out, err := fileoutline.Build(context.Background(), dir, tc.name)
			testutil.FailErr(t, "Build", err)
			if out.LogDigest != nil {
				t.Fatalf("log_digest present for code/config file: %+v", out.LogDigest)
			}
			if out.Source == "log" {
				t.Fatalf("source = log for %s", tc.name)
			}
		})
	}
}

func TestBuildAmbiguousProseNoLogDigest(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("plain prose line\n", 20)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "memo.qqq"), []byte(body), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "memo.qqq")
	testutil.FailErr(t, "Build", err)
	if out.LogDigest != nil {
		t.Fatalf("log_digest = %+v want nil for ambiguous prose", out.LogDigest)
	}
	if out.Source != "regex" {
		t.Fatalf("source = %q want regex", out.Source)
	}
}

func TestBuildLogDigestMarshalsOnWire(t *testing.T) {
	dir := t.TempDir()
	line := `{"level":"info","ts":"2024-01-15T10:00:00Z","msg":"event"}` + "\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "events.log"), []byte(line), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "events.log")
	testutil.FailErr(t, "Build", err)
	raw, err := json.Marshal(out)
	testutil.FailErr(t, "marshal", err)
	body := string(raw)
	if !strings.Contains(body, `"log_digest"`) || !strings.Contains(body, `"source":"log"`) {
		t.Fatalf("body = %s", body)
	}
}
