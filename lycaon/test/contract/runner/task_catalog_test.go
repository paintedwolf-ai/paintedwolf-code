package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func taskfile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "Taskfile.yml"))
	contractcheck.FailErr(t, "read Taskfile.yml", err)
	return string(data)
}

func taskBody(text, name string) string {
	needle := "\n  " + name + ":\n"
	i := strings.Index(text, needle)
	if i < 0 {
		return ""
	}
	rest := text[i+1:]
	lines := strings.Split(rest, "\n")
	var b strings.Builder
	b.WriteString(lines[0])
	b.WriteByte('\n')
	for _, ln := range lines[1:] {
		if strings.HasPrefix(ln, "  ") && !strings.HasPrefix(ln, "    ") && strings.Contains(ln, ":") {
			break
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return b.String()
}
