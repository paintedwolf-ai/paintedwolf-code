package contract

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/testcorpus"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var promptCodeLiteralRE = regexp.MustCompile(`(?m)Code:\s+([A-Z][A-Z0-9_]+)`)

func TestPromptHintCodesRegistered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	cfg, err := guidance.LoadHintConfigStock()
	contractcheck.FailErr(t, "load hint registry", err)

	registered := make(map[string]struct{}, len(cfg.HintCodes))
	for code := range cfg.HintCodes {
		registered[code] = struct{}{}
	}

	promptsDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "agents", "prompts")
	corpus, err := contractcheck.SourceLoader.Load(promptsDir, testcorpus.Options{Extensions: []string{".md"}})
	contractcheck.FailErr(t, "load prompt corpus", err)
	files := testcorpus.RequireNonEmpty(t, "prompt templates", corpus.Files())
	var violations []string
	for _, file := range files {
		content := file.Text()
		if strings.Contains(content, "Code: {{") {
			content = stripPongoCodeLines(content)
		}
		for _, m := range promptCodeLiteralRE.FindAllStringSubmatch(content, -1) {
			code := m[1]
			if _, ok := registered[code]; !ok {
				violations = append(violations, file.Rel+": unregistered Code: "+code)
			}
		}
	}
	contractcheck.FailViolations(t, "prompt templates reference hint codes missing from hint registry", violations)
}

func stripPongoCodeLines(content string) string {
	var b strings.Builder
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "Code: {{") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
