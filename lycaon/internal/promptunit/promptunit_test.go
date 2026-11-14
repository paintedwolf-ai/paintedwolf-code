package promptunit

import (
	"strings"
	"testing"
)

const goodUnit = `---
description: >-
  Running commands, builds, tests, or terminal sessions on the host under
  sandbox confinement.
slot: execution
order: 30
attaches: [command, terminal_open, verify]
modes: [investigate, orchestrate]
hosts: [coordinator]
---
### Host runner
Commands run on the real host.
`

func TestParseReadsFrontMatterAndBody(t *testing.T) {
	u, err := Parse("coordinator-host-runner", []byte(goodUnit))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if u.UnitID != "shared/units/coordinator-host-runner" || u.Ref != "units/coordinator-host-runner" {
		t.Fatalf("ids = %q %q", u.UnitID, u.Ref)
	}
	if u.Slot != SlotExecution || u.Order != 30 || len(u.Attaches) != 3 || len(u.Modes) != 2 || !u.HostsFor(HostCoordinator) || u.HostsFor(HostWorker) {
		t.Fatalf("unit = %+v", u)
	}
	if !strings.Contains(u.Description, "sandbox confinement.") || strings.Contains(u.Description, "\n") {
		t.Fatalf("description = %q", u.Description)
	}
	body := string(Body([]byte(goodUnit)))
	if !strings.HasPrefix(body, "### Host runner") || strings.Contains(body, "slot:") {
		t.Fatalf("body = %q", body)
	}
}

func TestParseAcceptsNeededWithFromExtensionPacksAndIgnoresIt(t *testing.T) {
	const unit = "---\ndescription: claims and receipts\nslot: evidence\nneeded_with: [capture_page, verify]\n---\nbody\n"
	u, err := Parse("claim-evidence", []byte(unit))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(u.Attaches) != 0 {
		t.Fatalf("unit = %+v", u)
	}
	if got := FromUnits([]Unit{u}).Rendered(Selection{Host: HostCoordinator}); len(got[SlotEvidence]) != 1 {
		t.Fatalf("a needed_with unit must render without its tools: %v", got)
	}
}

func TestParseRejectsBadHeaders(t *testing.T) {
	cases := map[string]string{
		"no fence":      "### Host runner\n",
		"no desc":       "---\nslot: conduct\n---\nbody\n",
		"bad slot":      "---\ndescription: x\nslot: sidebar\n---\nbody\n",
		"bad mode":      "---\ndescription: x\nslot: conduct\nmodes: [flying]\n---\nbody\n",
		"bad host":      "---\ndescription: x\nslot: conduct\nhosts: [den]\n---\nbody\n",
		"unknown field": "---\ndescription: x\nslot: conduct\ncolour: red\n---\nbody\n",
		"long desc":     "---\ndescription: " + strings.Repeat("word ", DescriptionMaxWords+1) + "\nslot: conduct\n---\nbody\n",
		"dup attach":    "---\ndescription: x\nslot: conduct\nattaches: [read, read]\n---\nbody\n",
	}
	for name, content := range cases {
		if _, err := Parse("unit-a", []byte(content)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := Parse("Bad_Stem", []byte(goodUnit)); err == nil {
		t.Error("stem: expected an error")
	}
}

func TestDefaultsFillHostsAndOrder(t *testing.T) {
	u, err := Parse("plain", []byte("---\ndescription: x\nslot: evidence\n---\nbody\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if u.Order != DefaultOrder || len(u.Hosts) != 1 || u.Hosts[0] != HostCoordinator {
		t.Fatalf("defaults = %+v", u)
	}
}

func fixtureCatalog() Catalog {
	return FromUnits([]Unit{
		{ID: "survey-ladder", Description: "orienting", Slot: SlotOrientation, Order: 10, Hosts: []Host{HostCoordinator, HostWorker}, Stock: true},
		{ID: "read-tool", Description: "reading", Slot: SlotOrientation, Order: 20, Attaches: []string{"read"}, Hosts: []Host{HostCoordinator, HostWorker}, Stock: true},
		{ID: "git-consent", Description: "git", Slot: SlotConduct, Attaches: []string{"git_commit", "git_status"}, Hosts: []Host{HostCoordinator}, Stock: true},
		{ID: "delegation", Description: "workers", Slot: SlotExecution, Order: 10, Attaches: []string{"task"}, Modes: []string{"investigate"}, Hosts: []Host{HostCoordinator}, Stock: true},
		{ID: "pack-unit", Description: "third party", Slot: SlotExecution, Order: 10, Hosts: []Host{HostCoordinator}},
		{ID: "runner", Description: "runner", Slot: SlotProcedures, Attaches: []string{"command"}, Hosts: []Host{HostCoordinator, HostWorker}, Stock: true},
	})
}

func ids(units []Unit) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, u.ID)
	}
	return out
}

func TestRenderedFollowsOfferedToolsHostAndMode(t *testing.T) {
	cat := fixtureCatalog()
	sel := Selection{Host: HostCoordinator, Mode: "investigate", Offered: map[string]bool{"read": true, "task": true}}
	got := cat.Rendered(sel)
	if want := []string{"survey-ladder", "read-tool"}; strings.Join(ids(got[SlotOrientation]), ",") != strings.Join(want, ",") {
		t.Fatalf("orientation = %v", ids(got[SlotOrientation]))
	}
	if len(got[SlotConduct]) != 0 {
		t.Fatalf("git consent rendered without git tools: %v", ids(got[SlotConduct]))
	}
	// Stock units lead ties on order; the pack unit follows.
	if want := "delegation,pack-unit"; strings.Join(ids(got[SlotExecution]), ",") != want {
		t.Fatalf("execution = %v", ids(got[SlotExecution]))
	}
	if len(got[SlotProcedures]) != 0 {
		t.Fatalf("runner rendered without command: %v", ids(got[SlotProcedures]))
	}
	// Orchestrate mode drops the investigate-only unit.
	sel.Mode = "orchestrate"
	if got := cat.Rendered(sel); strings.Join(ids(got[SlotExecution]), ",") != "pack-unit" {
		t.Fatalf("orchestrate execution = %v", ids(got[SlotExecution]))
	}
	// A worker host sees only worker units.
	sel = Selection{Host: HostWorker, Offered: map[string]bool{"read": true, "task": true, "command": true}}
	got = cat.Rendered(sel)
	if len(got[SlotExecution]) != 0 || len(got[SlotProcedures]) != 1 {
		t.Fatalf("worker render = %v / %v", ids(got[SlotExecution]), ids(got[SlotProcedures]))
	}
	// A unit attached to tools renders exactly while one of them is offered.
	sel = Selection{Host: HostCoordinator, Offered: map[string]bool{"task": true}}
	if got := cat.Rendered(sel); strings.Join(ids(got[SlotOrientation]), ",") != "survey-ladder" {
		t.Fatalf("render without read = %v", ids(got[SlotOrientation]))
	}
}

func TestRevisionTracksDescriptions(t *testing.T) {
	a := FromUnits([]Unit{{ID: "x", Description: "one", Slot: SlotConduct}})
	b := FromUnits([]Unit{{ID: "x", Description: "two", Slot: SlotConduct}})
	if a.Revision() == b.Revision() || a.Revision() == "" {
		t.Fatalf("revisions %q %q", a.Revision(), b.Revision())
	}
	if Fingerprint(a.Rendered(Selection{Host: HostCoordinator})) != "x" {
		t.Fatal("fingerprint should list rendered ids")
	}
}
