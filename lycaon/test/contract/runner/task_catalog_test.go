package contract

import (
	"encoding/json"
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

type verificationGoRecipe struct {
	Options []string `json:"options"`
	Exclude string   `json:"exclude"`
	Bundled bool     `json:"bundled"`
}

type verificationCatalogData struct {
	Groups map[string][]string             `json:"groups"`
	Go     map[string]verificationGoRecipe `json:"go"`
	CI     map[string]struct {
		Targets  []string `json:"targets"`
		Profiles []string `json:"profiles"`
	} `json:"ci"`
}

func verificationCatalog(t *testing.T) verificationCatalogData {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "scripts", "verification-plan.json"))
	contractcheck.FailErr(t, "read verification plan", err)
	var plan verificationCatalogData
	contractcheck.FailErr(t, "decode verification plan", json.Unmarshal(data, &plan))
	return plan
}
