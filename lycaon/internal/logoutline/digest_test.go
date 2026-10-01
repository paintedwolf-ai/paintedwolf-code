package logoutline_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBuildDigestEmptyOrGarbageReturnsErrNotLog(t *testing.T) {
	cases := []struct {
		name    string
		format  logoutline.LogFormat
		content []byte
	}{
		{"empty", logoutline.FormatJSONLines, nil},
		{"blank", logoutline.FormatJSONLines, []byte("  \n\n")},
		{"garbage", logoutline.FormatJSONLines, []byte("not json at all\nstill not\n")},
		{"none format", logoutline.FormatNone, []byte(`{"level":"info"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := logoutline.BuildDigest(tc.format, tc.content)
			if !errors.Is(err, logoutline.ErrNotLog) {
				t.Fatalf("err = %v want ErrNotLog", err)
			}
			if d != nil {
				t.Fatalf("digest = %+v want nil", d)
			}
		})
	}
}

func TestBuildDigestSyslogAuthClusterAndSeverityFacet(t *testing.T) {
	content := readTestdata(t, "digest_auth.log")
	d, err := logoutline.BuildDigest(logoutline.FormatSyslogRFC3164, content)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if d.Format != logoutline.FormatSyslogRFC3164 {
		t.Fatalf("format = %q", d.Format)
	}
	if d.RecordCount != 5 || d.ParsedCount != 5 {
		t.Fatalf("counts = %d/%d want 5/5", d.RecordCount, d.ParsedCount)
	}
	if d.TimeSpan == nil || d.TimeSpan.Start == "" || d.TimeSpan.End == "" {
		t.Fatal("time_span missing")
	}

	var severityFacet *logoutline.Facet
	for i := range d.Facets {
		if d.Facets[i].Key == "severity" {
			severityFacet = &d.Facets[i]
			break
		}
	}
	if severityFacet == nil {
		t.Fatalf("severity facet missing: %+v", d.Facets)
	}
	infoCount := facetCount(severityFacet, "info")
	if infoCount != 5 {
		t.Fatalf("severity info count = %d want 5", infoCount)
	}

	var failCluster *logoutline.Cluster
	for i := range d.Clusters {
		if strings.Contains(d.Clusters[i].Template, "Failed password") {
			failCluster = &d.Clusters[i]
			break
		}
	}
	if failCluster == nil {
		t.Fatalf("failure cluster missing: %+v", d.Clusters)
	}
	if failCluster.Count != 4 {
		t.Fatalf("failure cluster count = %d want 4", failCluster.Count)
	}
	if failCluster.FirstLine != 1 || failCluster.LastLine != 5 {
		t.Fatalf("anchors = %d-%d want 1-5", failCluster.FirstLine, failCluster.LastLine)
	}
	wantTmpl := "Failed password for invalid user <str> from <ipv4> port <num>"
	if failCluster.Template != wantTmpl {
		t.Fatalf("template = %q want %q", failCluster.Template, wantTmpl)
	}
}

func TestBuildDigestAccessStatusFacet(t *testing.T) {
	content := readTestdata(t, "digest_access.log")
	d, err := logoutline.BuildDigest(logoutline.FormatCLF, content)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	statusFacet := findFacet(d, "status")
	if statusFacet == nil {
		t.Fatalf("status facet missing: %+v", d.Facets)
	}
	if facetCount(statusFacet, "200") != 2 || facetCount(statusFacet, "404") != 2 {
		t.Fatalf("status counts = %+v", statusFacet.Values)
	}
	methodFacet := findFacet(d, "method")
	if methodFacet == nil {
		t.Fatal("method facet missing")
	}
	if facetCount(methodFacet, "GET") != 3 || facetCount(methodFacet, "POST") != 1 {
		t.Fatalf("method counts = %+v", methodFacet.Values)
	}
}

func TestBuildDigestJSONLinesFieldCoverage(t *testing.T) {
	content := readTestdata(t, "json_lines.log")
	d, err := logoutline.BuildDigest(logoutline.FormatJSONLines, content)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if d.ParsedCount != 3 {
		t.Fatalf("parsed = %d want 3", d.ParsedCount)
	}
	coverage := fieldCoverage(d, "level")
	if coverage != 66 {
		t.Fatalf("level coverage = %d want 66 (2 of 3 lines use level)", coverage)
	}
	if fieldCoverage(d, "severity") != 33 {
		t.Fatalf("severity coverage = %d want 33", fieldCoverage(d, "severity"))
	}
	if d.TimeSpan == nil {
		t.Fatal("time_span missing")
	}
}

func TestBuildDigestDeterministic(t *testing.T) {
	content := readTestdata(t, "digest_auth.log")
	d1, err := logoutline.BuildDigest(logoutline.FormatSyslogRFC3164, content)
	testutil.FailErr(t, "logoutline.BuildDigest failed", err)
	d2, err := logoutline.BuildDigest(logoutline.FormatSyslogRFC3164, content)
	testutil.FailErr(t, "logoutline.BuildDigest failed", err)
	b1, _ := json.Marshal(d1)
	b2, _ := json.Marshal(d2)
	if !bytes.Equal(b1, b2) {
		t.Fatalf("digest not deterministic:\n%s\n%s", b1, b2)
	}
}

func TestBuildDigestTruncatedOnHugeInput(t *testing.T) {
	line := []byte(`{"level":"info","ts":"2024-01-15T10:00:00Z","msg":"event"}` + "\n")
	var huge []byte
	for i := 0; i < 12000; i++ {
		huge = append(huge, line...)
	}
	d, err := logoutline.BuildDigest(logoutline.FormatJSONLines, huge)
	if err != nil {
		t.Fatalf("BuildDigest: %v", err)
	}
	if !d.Truncated {
		t.Fatal("Truncated = false want true")
	}
	if d.RecordCount != 10000 {
		t.Fatalf("RecordCount = %d want 10000", d.RecordCount)
	}
	if d.Clusters[0].FirstLine < 1 || d.Clusters[0].LastLine > 10000 {
		t.Fatalf("cluster anchors out of sample range: %+v", d.Clusters[0])
	}
}

func TestMaskMessageVariableTokens(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			"Failed password for invalid user admin from 203.0.113.7 port 55012",
			"Failed password for invalid user <str> from <ipv4> port <num>",
		},
		{
			`connection refused from 10.0.0.4`,
			"connection refused from <ipv4>",
		},
		{
			`request id 550e8400-e29b-41d4-a716-446655440000 failed`,
			"request id <uuid> failed",
		},
	}
	for _, tc := range cases {
		got := logoutline.MaskMessageForTest(tc.in)
		if got != tc.want {
			t.Fatalf("maskMessage(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildDigestAnchorsMatchSourceLines(t *testing.T) {
	content := readTestdata(t, "digest_auth.log")
	lines := bytes.Split(content, []byte("\n"))
	d, err := logoutline.BuildDigest(logoutline.FormatSyslogRFC3164, content)
	testutil.FailErr(t, "logoutline.BuildDigest failed", err)
	for _, c := range d.Clusters {
		if c.FirstLine < 1 || c.LastLine > len(lines) {
			t.Fatalf("cluster anchors %d-%d out of range for %d lines", c.FirstLine, c.LastLine, len(lines))
		}
		if c.FirstLine > c.LastLine {
			t.Fatalf("cluster first_line > last_line: %+v", c)
		}
	}
}

func findFacet(d *logoutline.Digest, key string) *logoutline.Facet {
	for i := range d.Facets {
		if d.Facets[i].Key == key {
			return &d.Facets[i]
		}
	}
	return nil
}

func facetCount(f *logoutline.Facet, value string) int {
	for _, v := range f.Values {
		if v.Value == value {
			return v.Count
		}
	}
	return 0
}

func fieldCoverage(d *logoutline.Digest, key string) int {
	for _, f := range d.Fields {
		if f.Key == key {
			return f.Coverage
		}
	}
	return -1
}
