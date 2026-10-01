package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestExecCommandRunnerNoNpmSpecialClassifiers(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")

	runnerDirs := []string{
		filepath.Join(lycaonRoot, "internal", "exec"),
		filepath.Join(lycaonRoot, "internal", "hostcmd"),
	}

	// Word boundaries exclude unrelated identifiers such as "component".
	forbidden := regexp.MustCompile(`(?i)(NPM_CONFIG|\bnpm\b|\bpnpm\b|\byarn\b|\bnpmrc\b|node_modules|\bcargo\b)`)

	var hits []string
	for _, dir := range runnerDirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(lycaonRoot, path)
			for i, line := range strings.Split(string(raw), "\n") {
				if forbidden.MatchString(line) {
					hits = append(hits, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				}
			}
			return nil
		})
		contractcheck.FailErr(t, "walk runner dir "+dir, err)
	}
	sort.Strings(hits)
	if len(hits) > 0 {
		t.Fatalf("exec/host-command runner references package-manager-specific env or classifiers: %v", hits)
	}
}
