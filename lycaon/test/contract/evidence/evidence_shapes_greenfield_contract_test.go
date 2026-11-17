package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestEvidenceShapesNoHardcodedCheckLabelMapInDen(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	denSrc := filepath.Join(root, "lycaon-den", "src")
	pattern := regexp.MustCompile(`(WORKER_|SYNTH_|INVEST_)[A-Z0-9_]+\s*:\s*['"]`)
	err := filepath.Walk(denSrc, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return nil
		}
		if strings.HasSuffix(path, ".test.ts") || strings.HasSuffix(path, ".test.tsx") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if pattern.Find(body) != nil {
			t.Errorf("%s: hardcoded grounding check-label map forbidden — use EvidenceMeta.ui_label from wire", path)
		}
		return nil
	})
	testutil.FailErr(t, "walk lycaon-den/src", err)
}
