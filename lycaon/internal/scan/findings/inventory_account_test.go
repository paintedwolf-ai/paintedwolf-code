package findings

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func accountFinding(scanner, rule string, level api.FindingLevel, paths ...string) api.SecurityFinding {
	f := api.SecurityFinding{RuleID: rule, Level: level, Tool: api.ToolDescriptor{DriverID: scanner}}
	for _, p := range paths {
		f.Locations = append(f.Locations, api.SecurityFindingLocation{URI: p})
	}
	return f
}

// Groups keep every place their findings were reported, so a path selector
// judges the whole group, and they run most severe first.
func TestInventoryGroups_KeepEveryPlace(t *testing.T) {
	groups := InventoryGroups([]api.SecurityFinding{
		accountFinding("secrets", "generic-key", api.FindingLevelMedium, "a_test.go"),
		accountFinding("secrets", "generic-key", api.FindingLevelHigh, "b_test.go", "a_test.go"),
		accountFinding("sca", "osv:CVE-1", api.FindingLevelCritical, "go.mod"),
	})
	if len(groups) != 2 || groups[0].Scanner != "sca" {
		t.Fatalf("groups = %+v, want the critical sca group first", groups)
	}
	secrets := groups[1]
	if secrets.Level != api.FindingLevelHigh || len(secrets.Paths) != 2 || secrets.Paths[0] != "a_test.go" {
		t.Fatalf("secrets group = %+v, want its most severe level and both paths once", secrets)
	}
}

// A group counts as linked when a claim or finding cites it, set aside when a
// set-aside names or selects it, and otherwise unaccounted. A path selector
// covers a group only when every place it was reported sits inside a glob.
func TestAccountInventory(t *testing.T) {
	groups := InventoryGroups([]api.SecurityFinding{
		accountFinding("sca", "osv:CVE-1", api.FindingLevelHigh, "go.mod"),
		accountFinding("secrets", "generic-key", api.FindingLevelHigh, "internal/x_test.go"),
		accountFinding("secrets", "aws-key", api.FindingLevelHigh, "internal/y_test.go", "cmd/main.go"),
		accountFinding("sast", "tls-off", api.FindingLevelHigh, "test/testdata/scan/vuln.go"),
	})
	ids := map[string]string{}
	for _, g := range groups {
		ids[g.RuleID] = g.ID
	}
	account := AccountInventory(groups,
		[]string{ids["osv:CVE-1"], "e865229e-f77c-48c3-b3a9-8a7dacb15338"},
		[]SetAside{
			{Scanner: "secrets", Paths: []string{"**/*_test.go"}, Reason: "test fixtures"},
			{GroupIDs: []string{ids["tls-off"], "group:gone"}, Reason: "scanner fixture"},
			{Scanner: "sast", Paths: []string{"docs/**"}, Reason: "docs"},
		})
	if account.Total() != 4 || account.LinkedCount() != 1 || account.SetAsideCount() != 2 {
		t.Fatalf("account = total %d linked %d set aside %d, want 4/1/2", account.Total(), account.LinkedCount(), account.SetAsideCount())
	}
	left := account.Unaccounted()
	if len(left) != 1 || left[0].RuleID != "aws-key" {
		t.Fatalf("unaccounted = %+v, want only the group reported outside the fixtures", left)
	}
	if got := account.SetAsideCounts; len(got) != 3 || got[0] != 1 || got[1] != 1 || got[2] != 0 {
		t.Fatalf("set-aside counts = %v, want 1, 1, 0", got)
	}
	if len(account.Unknown) != 2 {
		t.Fatalf("unknown = %v, want the scan id and the missing group", account.Unknown)
	}
}
