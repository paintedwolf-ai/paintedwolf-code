package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestJqEditIsADeclaredFileWriter(t *testing.T) {
	contract, ok := toolcontract.Lookup("jq_edit")
	if !ok {
		t.Fatal("jq_edit has no invocation contract")
	}
	if !contract.Supports(toolcontract.CapabilityFileChange) || contract.Concurrent() || !toolcontract.MutatesContent("jq_edit") {
		t.Fatalf("jq_edit contract = %+v, want a serial file_change content writer", contract)
	}
	if jq, _ := toolcontract.Lookup("jq"); jq.Supports(toolcontract.CapabilityFileChange) || toolcontract.MutatesContent("jq") {
		t.Fatalf("jq must stay read-only: %+v", jq)
	}
}

func TestJqEditWritesThroughFileChangeReview(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "config.json")
	before := "{\n  \"name\": \"api\",\n  \"port\": 8080\n}\n"
	testutil.FailErr(t, "seed", os.WriteFile(path, []byte(before), 0o644))
	tc := nativefixture.Context(dir)
	declined := errors.New("declined")
	var previews []tools.FileChange
	tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		previews = append(previews, changes...)
		if len(previews) == 1 {
			return declined
		}
		return nil
	}
	tool := &JqEditTool{Boundary: nativefixture.Boundary(t)}
	args := map[string]any{"path": "config.json", "query": ".port = 9090"}

	if _, err := tool.Run(context.Background(), args, tc); !errors.Is(err, declined) {
		t.Fatalf("declined review = %v", err)
	}
	current, err := os.ReadFile(path)
	testutil.FailErr(t, "read after decline", err)
	if string(current) != before {
		t.Fatalf("declined change landed: %s", current)
	}

	_, err = tool.Run(context.Background(), args, tc)
	testutil.FailErr(t, "jq_edit", err)
	want := "{\n  \"name\": \"api\",\n  \"port\": 9090\n}\n"
	current, err = os.ReadFile(path)
	testutil.FailErr(t, "read after approve", err)
	if string(current) != want {
		t.Fatalf("content = %q want %q", current, want)
	}
	last := previews[len(previews)-1]
	if last.Path != path || last.Preview.Before != before || last.Preview.After != want {
		t.Fatalf("review preview = %+v", last)
	}
}

func TestJqEditDestReadsSourceAndReviewsDestination(t *testing.T) {
	dir := canonicalTempDir(t)
	src := "name: api\nport: 8080\n"
	testutil.FailErr(t, "seed", os.WriteFile(filepath.Join(dir, "base.yaml"), []byte(src), 0o644))
	tc := nativefixture.Context(dir)
	var reviewed []string
	tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, c := range changes {
			reviewed = append(reviewed, c.Path)
		}
		return nil
	}
	tool := &JqEditTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": "base.yaml", "dest": "prod.yaml", "query": ".port = 443",
	}, tc)
	testutil.FailErr(t, "jq_edit dest", err)
	out, err := os.ReadFile(filepath.Join(dir, "prod.yaml"))
	testutil.FailErr(t, "read dest", err)
	if string(out) != "name: api\nport: 443\n" {
		t.Fatalf("dest = %q", out)
	}
	orig, err := os.ReadFile(filepath.Join(dir, "base.yaml"))
	testutil.FailErr(t, "read source", err)
	if string(orig) != src {
		t.Fatalf("source changed: %q", orig)
	}
	if len(reviewed) != 1 || reviewed[0] != filepath.Join(dir, "prod.yaml") {
		t.Fatalf("reviewed = %v, want only the destination", reviewed)
	}
}

func TestJqEditRefusalLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	src := "[package]\n# pinned\nversion = \"0.1.0\"\n"
	path := filepath.Join(dir, "Cargo.toml")
	testutil.FailErr(t, "seed", os.WriteFile(path, []byte(src), 0o644))
	tc := nativefixture.Context(dir)
	tc.Files.FileChangeReview = func(context.Context, []tools.FileChange) error {
		t.Fatal("a refused rewrite reached review")
		return nil
	}
	tool := &JqEditTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{"path": "Cargo.toml", "query": `.package.version = "0.2.0"`}, tc)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "JQ_EDIT_LOSSY" {
		t.Fatalf("err = %v want JQ_EDIT_LOSSY", err)
	}
	current, err := os.ReadFile(path)
	testutil.FailErr(t, "read", err)
	if string(current) != src || !strings.Contains(string(current), "# pinned") {
		t.Fatalf("file changed: %q", current)
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp dir", err)
	return dir
}

func TestJqEditVarsBindsVariablesSafely(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "config.json")
	before := "{\n  \"name\": \"api\"\n}\n"
	testutil.FailErr(t, "seed", os.WriteFile(path, []byte(before), 0o644))
	tc := nativefixture.Context(dir)
	tool := &JqEditTool{Boundary: nativefixture.Boundary(t)}
	const secretVal = `my"secret'with\special$chars and \n newlines`
	args := map[string]any{
		"path":  "config.json",
		"query": ".credentials.api_key = $secret",
		"vars":  map[string]any{"secret": secretVal},
	}
	_, err := tool.Run(context.Background(), args, tc)
	testutil.FailErr(t, "jq_edit with vars", err)
	current, err := os.ReadFile(path)
	testutil.FailErr(t, "read after edit", err)
	if !strings.Contains(string(current), "api_key") {
		t.Fatalf("api_key not found in result: %s", string(current))
	}
}
