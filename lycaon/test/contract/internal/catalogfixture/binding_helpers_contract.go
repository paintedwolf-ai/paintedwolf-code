package catalogfixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// StockGuidanceDirs returns every stock pack guidance/ directory.
func StockGuidanceDirs(t *testing.T) []extpacks.Source {
	t.Helper()
	packs, err := extpacks.DiscoverStock()
	testutil.FailErr(t, "DiscoverStock", err)
	return extpacks.KindDirs(packs, "guidance")
}

// StockWorkflowRoots returns every stock pack workflows/ directory.
func StockWorkflowRoots(t *testing.T) []extpacks.Source {
	t.Helper()
	packs, err := extpacks.DiscoverStock()
	testutil.FailErr(t, "DiscoverStock", err)
	return extpacks.KindDirs(packs, "workflows")
}

// StockGuidancePath returns <stem>.md under any stock guidance/ directory.
func StockGuidancePath(stem string) (extpacks.Source, error) {
	stem = strings.TrimSuffix(strings.TrimSpace(stem), ".md")
	packs, err := extpacks.DiscoverStock()
	if err != nil {
		return extpacks.Source{}, err
	}
	var lastErr error
	for _, dir := range extpacks.KindDirs(packs, "guidance") {
		path := dir.Join(stem + ".md")
		if _, err := path.Stat(); err == nil {
			return path, nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return extpacks.Source{}, lastErr
}

// FindStockPolicyFile returns policy/<CODE>.yaml across stock packs.
func FindStockPolicyFile(t *testing.T, code string) extpacks.Source {
	t.Helper()
	code = strings.TrimSpace(code)
	packs, err := extpacks.DiscoverStock()
	testutil.FailErr(t, "DiscoverStock", err)
	for _, dir := range extpacks.KindDirs(packs, "policy") {
		path := dir.Join(code + ".yaml")
		if _, err := path.Stat(); err == nil {
			return path
		}
	}
	t.Fatalf("missing stock policy %s.yaml", code)
	return extpacks.Source{}
}

// FindStockAgentPrompt returns filename under any stock pack agents/prompts/.
func FindStockAgentPrompt(t *testing.T, filename string) extpacks.Source {
	t.Helper()
	path, err := StockAgentPromptPath(filename)
	testutil.FailErr(t, "StockAgentPromptPath "+filename, err)
	return path
}

func StockAgentPromptPath(filename string) (extpacks.Source, error) {
	filename = strings.TrimSpace(filepath.Base(filename))
	packs, err := extpacks.DiscoverStock()
	if err != nil {
		return extpacks.Source{}, err
	}
	var lastErr error
	for _, p := range packs {
		path := p.Root.Join("agents", "prompts", filename)
		if _, err := path.Stat(); err == nil {
			return path, nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return extpacks.Source{}, lastErr
}

func StockAgentPromptTrimRel(filename string) string {
	path, err := StockAgentPromptPath(filename)
	if err != nil {
		return config.PlatformPrompts.Join(filename).String()
	}
	return path.String()
}

func LoadInformBindings(t *testing.T) *anchor.Registry {
	t.Helper()
	reg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "LoadRegistryFromConfigRoot", err)
	if reg == nil {
		t.Fatal("nil Binding registry")
	}
	return reg
}

func BindingShortID(render string) string {
	render = strings.TrimSpace(render)
	if strings.HasPrefix(render, "coordinator-") {
		return strings.TrimPrefix(render, "coordinator-")
	}
	return render
}

func MustParseAnchor(t *testing.T, id string) anchor.ID {
	t.Helper()
	a, ok := anchor.ParseID(id)
	if !ok {
		t.Fatalf("ParseID(%q) failed", id)
	}
	return a
}

func AssertNoKickConstFiles(t *testing.T) {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	for _, rel := range []string{
		"lycaon/internal/coordinator/kick/ids.go",
		"lycaon/internal/coordinator/kick/worker_ids.go",
		"lycaon/internal/coordinator/kick/template_ref.go",
		"lycaon/config/packs/painted-wolf/platform/guidance/_contract.yaml",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("%s must be deleted", rel)
		}
	}
}
