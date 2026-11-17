package contract

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/egressclass"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// These client constructions omit request deadlines.
var unboundedClientPatterns = []string{
	"http.DefaultClient",
	"http.Get(",
	"http.Post(",
	"http.Head(",
	"http.PostForm(",
	"&http.Client{}",
}

// httpclientPkgDir owns HTTP client construction.
const httpclientPkgDir = "internal/httpclient"

func TestNoUnboundedHTTPClients(t *testing.T) {
	t.Parallel()
	root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
	internalDir := filepath.Join(root, "internal")

	var offenders []string

	err := filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if strings.HasPrefix(filepath.ToSlash(rel), httpclientPkgDir+"/") {
			return nil
		}

		content := contractcheck.ReadRepoFile(t, root, rel)
		for i, line := range strings.Split(content, "\n") {
			for _, pattern := range unboundedClientPatterns {
				if strings.Contains(line, pattern) {
					offenders = append(offenders, fmt.Sprintf("%s:%d — %s", filepath.ToSlash(rel), i+1, pattern))
				}
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal for unbounded clients", err)

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("unbounded HTTP clients in lycaon/internal (%d):\n  %s\n\n"+
			"Use httpclient.Bounded for request/response, httpclient.Streaming for LLM completions "+
			"(never a total timeout on a stream), or httpclient.Download for large fixed-host fetches.",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// Declared notice codes belong to the catalog and API error surface.
func TestEgressFailureCodesHaveNotices(t *testing.T) {
	t.Parallel()
	cfg := loadUserNoticeConfig(t)

	var violations []string
	for _, c := range egressclass.All() {
		if c.NoticeCode == "" {
			continue
		}
		entry, ok := cfg.UserNotices[c.NoticeCode]
		if !ok {
			violations = append(violations, fmt.Sprintf(
				"egress class %s NoticeCode %q has no row in host/user-notices", c.ID, c.NoticeCode))
			continue
		}
		if !entry.IsUserVisible() {
			violations = append(violations, fmt.Sprintf(
				"egress class %s NoticeCode %q is user_visible:false — the user is told nothing", c.ID, c.NoticeCode))
		}
	}
	contractcheck.FailViolations(t, "egress notice catalog drift", violations)
}
