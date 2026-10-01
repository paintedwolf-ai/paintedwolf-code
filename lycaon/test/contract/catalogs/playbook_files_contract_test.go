package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPlaybookFilesExistForContract(t *testing.T) {
	t.Parallel()
	contract, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	packs, err := extpacks.DiscoverStock()
	contractcheck.FailErr(t, "DiscoverStock", err)
	playbookDirs := extpacks.KindDirs(packs, "playbooks")
	seen := make(map[string]struct{})
	for _, def := range contract.Agents {
		for _, id := range def.PlaybookIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			found := false
			for _, playbooksDir := range playbookDirs {
				if _, err := playbooksDir.Join(id + ".yaml").Stat(); err == nil {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("playbook %q not found in stock playbooks/", id)
			}
		}
	}
}

func TestPlaybookFileCount(t *testing.T) {
	t.Parallel()
	packs, err := extpacks.DiscoverStock()
	contractcheck.FailErr(t, "DiscoverStock", err)
	var count int
	for _, playbooksDir := range extpacks.KindDirs(packs, "playbooks") {
		entries, err := playbooksDir.List()
		contractcheck.FailErr(t, "read directory entries", err)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
				count++
			}
		}
	}
	if count < 12 {
		t.Fatalf("playbooks = %d want at least 12 (default + 11 addenda)", count)
	}
}

func TestPlaybookMatcherLoadsFromModuleRoot(t *testing.T) {
	t.Parallel()
	contract, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "prompts.LoadPersonaContract failed", err)
	m, err := prompts.LoadPlaybookMatcherEffective(contract)
	contractcheck.FailErr(t, "prompts.LoadPlaybookMatcherEffective failed", err)
	if m == nil {
		t.Fatal("nil matcher")
	}
}
