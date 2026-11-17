package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Copy is data. A notice added straight to TypeScript skips review and the scope
// table, so the catalog has to be the only source.
func TestClientNoticeCopyIsNotInlineInDen(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon-den/src/notices/client-notices.ts")
	raw, err := os.ReadFile(path)
	testutil.FailErr(t, "read client-notices.ts", err)
	src := string(raw)

	// A copy field outside a comment means the catalog was bypassed.
	for _, field := range []string{"title:", "message:", "suggestedAction:"} {
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if strings.Contains(trimmed, field) {
				t.Errorf("client-notices.ts declares %s inline — copy belongs in %s:\n  %s",
					field, clientNoticesDirRel, trimmed)
			}
		}
	}
	// It reads the catalog for the error message without re-exporting it;
	// consumers import generated catalogs directly.
	if !strings.Contains(src, `from "./client-notices.generated.ts"`) {
		t.Error("client-notices.ts no longer reads the generated catalog")
	}
	if strings.Contains(src, `export {`) || strings.Contains(src, "export *") {
		t.Error("client-notices.ts re-exports the generated catalog — consumers import it directly")
	}
}

const clientNoticesDirRel = "lycaon/config/packs/painted-wolf/platform/host/client-notices/"
