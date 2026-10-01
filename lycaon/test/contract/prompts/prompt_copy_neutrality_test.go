package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Agent-facing prompt copy must stay neutral: no product names, engine codename,
// or UI-surface references the agent cannot act on. Functional ids stay legal —
// the patterns below do not match lowercase `mcp_lycaon_*` tool ids, `.paintedwolf/`
// paths, or `lycaon-*` file and rule ids.

// agentFacingUnitKinds are the pack subtrees rendered into agent context, derived
// per pack so a new pack is covered without an edit here. Kinds absent from this
// list are operator surfaces (host/, contributions/, approvals/, themes/).
var agentFacingUnitKinds = []string{
	"agents", "shared", "policy", "playbooks", "workflows", "guidance", "skills",
	filepath.Join("tools", "schemas"),
}

var forbiddenAgentCopyPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bDen\b`),            // frontend product name
	regexp.MustCompile(`Lycaon`),             // engine codename (lowercase functional ids are allowed)
	regexp.MustCompile(`(?i)painted wolf`),   // product name
	regexp.MustCompile(`(?i)\bsidecar\b`),    // engine internals the user cannot act on either
	regexp.MustCompile(`(?i)progress strip`), // UI surface — say "progress checklist"
	regexp.MustCompile(`(?i)plan modal`),     // UI surface
	regexp.MustCompile(`(?i)project editor`), // UI surface
	regexp.MustCompile(`(?i)review modal`),   // UI surface
	regexp.MustCompile(`(?i)review bar`),     // UI surface — say what the user does, not where
	regexp.MustCompile(`(?i)notice rail`),    // UI surface
	regexp.MustCompile(`(?i)\w+ drawer\b`),   // UI surface — worker drawer, Workflows drawer
	regexp.MustCompile(`(?i)composer dock`),  // UI surface
	regexp.MustCompile(`(?i)host inspector`), // UI surface
	regexp.MustCompile(`(?i)proposal-card`),  // UI surface
	regexp.MustCompile(`lycaon-tools\.yaml`), // internal manifest the agent cannot read
}

func TestAgentFacingConfigCopyNeutral(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	packsDir := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf")

	packs, err := os.ReadDir(packsDir)
	contractcheck.FailErr(t, "read packs dir", err)

	var scanned int
	var violations []string
	for _, pack := range packs {
		if !pack.IsDir() {
			continue
		}
		for _, kind := range agentFacingUnitKinds {
			dir := filepath.Join(packsDir, pack.Name(), kind)
			if _, statErr := os.Stat(dir); statErr != nil {
				continue
			}
			walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				// Sigma rules carry an author: field and are never rendered as prompt copy.
				if d.IsDir() && d.Name() == "detection-packs" {
					return filepath.SkipDir
				}
				if d.IsDir() || !agentCopyFile(path) {
					return nil
				}
				scanned++
				violations = append(violations, neutralityHits(t, root, path)...)
				return nil
			})
			contractcheck.FailErr(t, "walk "+dir, walkErr)
		}
	}

	if scanned == 0 {
		t.Fatal("scanned no agent-facing copy — the surface derivation is broken and this contract would pass anything")
	}
	sort.Strings(violations)
	contractcheck.FailViolations(t, "agent-facing copy must stay neutral — an agent cannot act on a product name, "+
		"an engine internal, or a UI surface it cannot see", violations)
}

func agentCopyFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".yaml", ".yml":
		return true
	}
	return false
}

// neutralityHits reports forbidden matches, skipping comment lines: a `#` note in
// a catalog file is written for whoever maintains it, not for the model.
func neutralityHits(t *testing.T, root, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read "+path, err)
	rel, _ := filepath.Rel(root, path)
	var out []string
	for i, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, pattern := range forbiddenAgentCopyPatterns {
			if m := pattern.FindString(line); m != "" {
				out = append(out, filepath.ToSlash(rel)+":"+strconv.Itoa(i+1)+" "+pattern.String()+" matched "+m)
			}
		}
	}
	return out
}
