package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var forbiddenAPIErrorPhrases = []string{
	"session mode",
	"invalid session mode",
	"mode is required",
}

func TestAPIHandlersUsePostureErrorVocabulary(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	apiRoot := filepath.Join(root, "lycaon", "internal", "api")
	exts := map[string]struct{}{".go": {}}
	var violations []string
	err := contractcheck.WalkFiles(apiRoot, exts, true, func(path string, data []byte) error {
		for i, line := range strings.Split(string(data), "\n") {
			trim := strings.TrimSpace(line)
			if contractcheck.SkipCommentLine(trim) {
				continue
			}
			lower := strings.ToLower(line)
			for _, phrase := range forbiddenAPIErrorPhrases {
				if strings.Contains(lower, phrase) {
					violations = append(violations, contractcheck.FmtLine(path, i+1, phrase, trim))
				}
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk repository files", err)
	for _, v := range violations {
		t.Error(v)
	}
}

func TestCreateSessionRequiresPostureField(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", "api", "sessionadmin", "session_create.go"))
	contractcheck.FailErr(t, "read file", err)
	text := string(data)
	for _, required := range []string{"req.Posture", `"posture is required"`, `"invalid session posture"`, "validSessionPosture"} {
		if !strings.Contains(text, required) {
			t.Errorf("session_create.go missing %q", required)
		}
	}
}
