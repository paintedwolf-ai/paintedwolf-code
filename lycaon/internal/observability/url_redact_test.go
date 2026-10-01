package observability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRedactURLForLogTable(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://x/?token=secret", "https://x/?token=REDACTED"},
		{"https://x/?api_key=k&q=1", "https://x/?api_key=REDACTED&q=1"},
		{"https://x/?page=1", "https://x/?page=1"},
		{"https://x/?X-Amz-Signature=abc&q=2", "https://x/?X-Amz-Signature=REDACTED&q=2"},
		{"https://x/?otp=123456", "https://x/?otp=REDACTED"},
		{"https://x/?sid=abc", "https://x/?sid=REDACTED"},
		{"https://x/?path=intro", "https://x/?path=intro"},
	}
	for _, tc := range cases {
		if got := RedactURLForLog(tc.in); got != tc.want {
			t.Fatalf("RedactURLForLog(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactQueryForLog(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"page=1", "page=1"},
		{"token=secret", "token=REDACTED"},
		{"otp=1&page=2", "otp=REDACTED&page=2"},
		// Malformed queries must fail safe, never echo the raw string.
		{"a=1;token=secret", ""},                 // Drop the unparseable ';' segment.
		{"token=secret&x=%zz", "token=REDACTED"}, // bad '%' escape: still redact the parsed key
	}
	for _, tc := range cases {
		if got := RedactQueryForLog(tc.in); got != tc.want {
			t.Fatalf("RedactQueryForLog(%q)=%q want %q", tc.in, got, tc.want)
		}
		if strings.Contains(RedactQueryForLog(tc.in), "secret") {
			t.Fatalf("RedactQueryForLog(%q) leaked a secret: %q", tc.in, RedactQueryForLog(tc.in))
		}
	}
}

func TestLogWebSearchFetchNeverLogsRawSecrets(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "web-search.jsonl")
	t.Setenv("LYCAON_WEBSEARCH_DEBUG", "1")
	t.Setenv("LYCAON_WEBSEARCH_DEBUG_FILE", logPath)
	CloseWebSearchDebug()

	secret := "should-never-appear-in-log"
	LogWebSearchFetch(WebSearchFetchCapture{
		SearchID: "s1",
		Phase:    "frontier",
		Kind:     "probe",
		URL:      "https://docs.example.com/guide?token=" + secret + "&path=intro",
		Host:     "docs.example.com",
		FetchMs:  12,
		Verdict:  "hit",
	})

	data, err := os.ReadFile(logPath)
	testutil.FailErr(t, "read web-search debug", err)
	if len(data) == 0 {
		t.Fatal("expected a debug row")
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("raw secret leaked into debug log: %s", data)
	}
	var entry webSearchFetchEntry
	line := strings.TrimSpace(string(data))
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(line), &entry))
	if !strings.Contains(entry.URL, "token=REDACTED") {
		t.Fatalf("expected redacted token in url, got %q", entry.URL)
	}
	if !strings.Contains(entry.URL, "path=intro") {
		t.Fatalf("expected normal query preserved, got %q", entry.URL)
	}
}
