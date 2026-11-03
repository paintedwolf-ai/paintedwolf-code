package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"os"
	"path/filepath"
	"testing"
)

// Declined credential reviews leave the file unchanged.
func TestContentMutationToolsReviewCredentialPaths(t *testing.T) {
	paths := []string{
		".env",
		".env.production",
		".ssh/config",
		"deploy/id_rsa",
		".npmrc",
		".aws/credentials",
		"certs/server.pem",
	}
	for _, rel := range paths {
		t.Run("write/"+rel, func(t *testing.T) {
			dir := seedCredentialFile(t, rel)
			tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
			_, err := tool.Run(context.Background(), map[string]any{
				"path": rel, "content": "",
			}, decliningReview(t, dir, rel))
			assertReviewDeclined(t, err, rel)
			assertFileUnchanged(t, filepath.Join(dir, filepath.FromSlash(rel)))
		})
	}
}

// The host family reaches the same review for the same paths.
func TestHostMutationToolsReviewCredentialPaths(t *testing.T) {
	dir := seedCredentialFile(t, ".env")
	tool := &DeleteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{".env"},
	}, decliningReview(t, dir, ".env"))
	assertReviewDeclined(t, err, ".env")
	assertFileUnchanged(t, filepath.Join(dir, ".env"))
}

// A published template is not credential material; adding a key to it is
// ordinary work that stays writable.
func TestCredentialTemplateStaysWritable(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	if _, err := tool.Run(context.Background(), map[string]any{
		"path": ".env.example", "content": "API_KEY=\n",
	}, nativefixture.Context(dir)); err != nil {
		testutil.FailErr(t, "write .env.example", err)
	}
}

func seedCredentialFile(t *testing.T, rel string) string {
	t.Helper()
	dir := t.TempDir()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir parent", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "seed file", os.WriteFile(abs, []byte("TOKEN=SECRET\n"), 0o600))
	return dir
}

var errReviewDeclined = errors.New("review declined")

// decliningReview records that rel reached review, then declines it. Parent
// directories an archive creates reach review first and are not the subject.
func decliningReview(t *testing.T, dir, rel string) tools.ToolContext {
	t.Helper()
	tctx := nativefixture.Context(dir)
	want := fspath.CanonicalPath(filepath.Join(dir, filepath.FromSlash(rel)))
	reviewed := false
	tctx.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, change := range changes {
			if fspath.CanonicalPath(change.Path) == want {
				reviewed = true
				return errReviewDeclined
			}
		}
		return nil
	}
	t.Cleanup(func() {
		if !reviewed {
			t.Errorf("%s never reached review", rel)
		}
	})
	return tctx
}

func assertReviewDeclined(t *testing.T, err error, rel string) {
	t.Helper()
	if !errors.Is(err, errReviewDeclined) {
		var reject *toolrejection.ToolReject
		if errors.As(err, &reject) {
			t.Fatalf("mutating %s was refused with %s; credential writes must reach review", rel, reject.Code)
		}
		t.Fatalf("mutating %s: err = %v, want the declined review", rel, err)
	}
}

func assertFileUnchanged(t *testing.T, abs string) {
	t.Helper()
	got, err := os.ReadFile(abs) // #nosec G304 -- test-owned temp path.
	testutil.FailErr(t, "read back", err)
	if string(got) != "TOKEN=SECRET\n" && string(got) != "package deploy\n\nvar Key = \"SECRET\"\n" {
		t.Fatalf("file was mutated: %q", string(got))
	}
}
