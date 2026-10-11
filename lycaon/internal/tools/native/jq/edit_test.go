package jq

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func runEdit(t *testing.T, path, text, query string) (EditResult, error) {
	t.Helper()
	return Edit(context.Background(), EditRequest{Path: path, Query: query, Text: text})
}

func mustEdit(t *testing.T, path, text, query string) string {
	t.Helper()
	res, err := runEdit(t, path, text, query)
	testutil.FailErr(t, "edit "+path, err)
	return res.Text
}

func wantReject(t *testing.T, err error, code string) *toolrejection.ToolReject {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	return reject
}

func TestEditJSONPreservesLiteralsOrderAndLayout(t *testing.T) {
	src := "{\n\t\"zeta\": 12345678901234567890,\n\t\"alpha\": 1.10,\n\t\"url\": \"a<b>&c\",\n\t\"count\": 1\n}\n"
	got := mustEdit(t, "config.json", src, ".count = 2")
	want := "{\n\t\"zeta\": 12345678901234567890,\n\t\"alpha\": 1.10,\n\t\"url\": \"a<b>&c\",\n\t\"count\": 2\n}\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEditJSONNewKeysFollowSourceKeys(t *testing.T) {
	got := mustEdit(t, "a.json", `{"b":1,"a":2}`, `.c = 3 | .aa = 4`)
	if got != `{"b":1,"a":2,"aa":4,"c":3}` {
		t.Fatalf("got %s", got)
	}
}

func TestEditJSONLinesFiltersRecords(t *testing.T) {
	src := "{\"id\":1,\"keep\":true}\n{\"id\":2,\"keep\":false}\n{\"id\":3,\"keep\":true}\n"
	got := mustEdit(t, "events.jsonl", src, "select(.keep)")
	if got != "{\"id\":1,\"keep\":true}\n{\"id\":3,\"keep\":true}\n" {
		t.Fatalf("got %q", got)
	}
}

func TestEditRefusesTruncatedStream(t *testing.T) {
	var b strings.Builder
	for i := 0; i <= safecmd.JQScanCap; i++ {
		b.WriteString("{}\n")
	}
	_, err := runEdit(t, "events.jsonl", b.String(), ".")
	wantReject(t, err, "JQ_EDIT_STREAM_CAP")
}

func TestEditRefusesEmptyAndFannedOutResults(t *testing.T) {
	_, err := runEdit(t, "a.json", `{"a":1}`, "empty")
	wantReject(t, err, "JQ_EDIT_EMPTY")
	_, err = runEdit(t, "a.json", `{"a":[1,2]}`, ".a[]")
	wantReject(t, err, "JQ_EDIT_RESULT_COUNT")
}

func TestEditYAMLKeepsCommentsAndUnchangedLiterals(t *testing.T) {
	src := "# service settings\nname: api # the service\nversion: 1.0\nport: 8080\nlabels:\n  - web\n  - edge # public\n"
	got := mustEdit(t, "svc.yaml", src, `.port = 9090 | .labels += ["new"]`)
	for _, want := range []string{"# service settings", "# the service", "version: 1.0", "port: 9090", "- edge # public", "- new"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestEditYAMLChangedScalarKeepsItsComment(t *testing.T) {
	got := mustEdit(t, "a.yaml", "replicas: 2 # scaled by ops\n", ".replicas = 3")
	if got != "replicas: 3 # scaled by ops\n" {
		t.Fatalf("got %q", got)
	}
}

func TestEditYAMLRefusesAnchors(t *testing.T) {
	src := "base: &base\n  image: api\nweb:\n  <<: *base\n"
	_, err := runEdit(t, "compose.yaml", src, ".web.image = \"x\"")
	reject := wantReject(t, err, "JQ_EDIT_LOSSY")
	if reject.Data["kind"] != "anchor" {
		t.Fatalf("kind = %v", reject.Data["kind"])
	}
}

func TestEditYAMLRefusesDroppingATag(t *testing.T) {
	_, err := runEdit(t, "t.yaml", "ref: !Ref bucket\n", `.ref = "other"`)
	reject := wantReject(t, err, "JQ_EDIT_LOSSY")
	if reject.Data["kind"] != "tag" {
		t.Fatalf("kind = %v", reject.Data["kind"])
	}
	got := mustEdit(t, "t.yaml", "ref: !Ref bucket\nn: 1\n", ".n = 2")
	if !strings.Contains(got, "!Ref bucket") {
		t.Fatalf("unchanged tag lost:\n%s", got)
	}
}

func TestEditYAMLStreamMapsResultsToTheirDocuments(t *testing.T) {
	src := "kind: Service # svc\nname: a\n---\nkind: Deployment\nname: b\n"
	got := mustEdit(t, "k8s.yaml", src, `select(.kind == "Service") | .name = "c"`)
	if !strings.Contains(got, "kind: Service # svc") || !strings.Contains(got, "name: c") || strings.Contains(got, "Deployment") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestEditTOMLPreservesNumberTypesOrderAndInlineTables(t *testing.T) {
	src := "[package]\nname = \"demo\"\nversion = \"0.1.0\"\nedition = 2021\nratio = 1.0\n\n[dependencies]\nserde = { version = \"1\", features = [\"derive\"] }\n"
	got := mustEdit(t, "Cargo.toml", src, `.package.version = "0.2.0"`)
	want := "[package]\nname = \"demo\"\nversion = \"0.2.0\"\nedition = 2021\nratio = 1.0\n\n[dependencies]\nserde = { version = \"1\", features = [\"derive\"] }\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEditTOMLArrayTablesStaySections(t *testing.T) {
	src := "[[bin]]\nname = \"a\"\n\n[[bin]]\nname = \"b\"\n"
	got := mustEdit(t, "Cargo.toml", src, `.bin += [{"name": "c"}]`)
	if strings.Count(got, "[[bin]]") != 3 || !strings.Contains(got, "name = \"c\"") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestEditTOMLRefusesCommentsAndNull(t *testing.T) {
	_, err := runEdit(t, "Cargo.toml", "[package]\n# pinned\nname = \"demo\"\n", `.package.name = "x"`)
	reject := wantReject(t, err, "JQ_EDIT_LOSSY")
	if reject.Data["kind"] != "comment" || reject.Data["line"] != 2 {
		t.Fatalf("data = %v", reject.Data)
	}
	_, err = runEdit(t, "Cargo.toml", "[package]\nname = \"demo\"\n", `.package.name = null`)
	wantReject(t, err, "JQ_EDIT_ENCODE")
}
