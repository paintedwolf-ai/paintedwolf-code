package promptunit

import (
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/flosch/pongo2/v6"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/pongoplain"
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
		"no fence":       "### Host runner\n",
		"no desc":        "---\nslot: conduct\n---\nbody\n",
		"bad slot":       "---\ndescription: x\nslot: sidebar\n---\nbody\n",
		"bad mode":       "---\ndescription: x\nslot: conduct\nmodes: [flying]\n---\nbody\n",
		"bad host":       "---\ndescription: x\nslot: conduct\nhosts: [den]\n---\nbody\n",
		"unknown field":  "---\ndescription: x\nslot: conduct\ncolour: red\n---\nbody\n",
		"long desc":      "---\ndescription: " + strings.Repeat("word ", DescriptionMaxWords+1) + "\nslot: conduct\n---\nbody\n",
		"dup attach":     "---\ndescription: x\nslot: conduct\nattaches: [command, command]\n---\nbody\n",
		"unknown attach": "---\ndescription: x\nslot: conduct\nattaches: [unknown_tool]\n---\nbody\n",
		"operation name": "---\ndescription: x\nslot: conduct\nattaches: [read]\n---\nbody\n",
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

func TestRenderedFollowsHeldToolsHostAndMode(t *testing.T) {
	cat := fixtureCatalog()
	sel := Selection{Host: HostCoordinator, Mode: "investigate", Held: map[string]bool{"read": true, "task": true}}
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
	sel = Selection{Host: HostWorker, Held: map[string]bool{"read": true, "task": true, "command": true}}
	got = cat.Rendered(sel)
	if len(got[SlotExecution]) != 0 || len(got[SlotProcedures]) != 1 {
		t.Fatalf("worker render = %v / %v", ids(got[SlotExecution]), ids(got[SlotProcedures]))
	}
	// A unit attached to tools renders while one of them is held.
	sel = Selection{Host: HostCoordinator, Held: map[string]bool{"task": true}}
	if got := cat.Rendered(sel); strings.Join(ids(got[SlotOrientation]), ",") != "survey-ladder" {
		t.Fatalf("render without read = %v", ids(got[SlotOrientation]))
	}
}

func TestParseAcceptsRegisteredToolsAndReachMarks(t *testing.T) {
	const unit = `---
description: reach marks and tools
slot: procedures
attaches: [command_mutation, command_external_exec, command_external_exec_selector, command_network_egress, restore_version]
---
body
`
	u, err := Parse("reach-unit", []byte(unit))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(u.Attaches) != 5 {
		t.Fatalf("attaches = %v", u.Attaches)
	}
}

func TestLoadRejectsUnknownAttachByPackAuthority(t *testing.T) {
	badBody := []byte("---\ndescription: bad attach\nslot: conduct\nattaches: [not_a_registered_tool]\n---\nbody\n")
	goodBody := []byte("---\ndescription: good attach\nslot: conduct\nattaches: [command_mutation]\n---\nbody\n")

	stockCat := &extpacks.EffectiveCatalog{
		Packs: []extpacks.PackSummary{
			{ID: extpacks.PlatformPackID, Bundled: true, Contributing: true},
		},
		Loaded: map[string]extpacks.UnitEffective{
			"shared/units/bad-unit": {
				ID:           "shared/units/bad-unit",
				WinnerPackID: extpacks.PlatformPackID,
				Content:      badBody,
			},
		},
	}
	if _, err := Load(stockCat); err == nil {
		t.Fatal("stock unit with unknown attach must fail Load")
	}

	thirdPartyCat := &extpacks.EffectiveCatalog{
		Packs: []extpacks.PackSummary{
			{ID: extpacks.PlatformPackID, Bundled: true, Contributing: true},
			{ID: "acme/custom", Bundled: false, Contributing: true},
		},
		Loaded: map[string]extpacks.UnitEffective{
			"shared/units/good-unit": {
				ID:           "shared/units/good-unit",
				WinnerPackID: extpacks.PlatformPackID,
				Content:      goodBody,
			},
			"shared/units/bad-pack-unit": {
				ID:           "shared/units/bad-pack-unit",
				WinnerPackID: "acme/custom",
				Content:      badBody,
			},
		},
	}
	cat, err := Load(thirdPartyCat)
	if err != nil {
		t.Fatalf("third-party unknown attach must not fail Load: %v", err)
	}
	if cat.Len() != 1 {
		t.Fatalf("loaded units = %d, want 1", cat.Len())
	}
	if len(cat.Rejected) != 1 || cat.Rejected[0].UnitID != "shared/units/bad-pack-unit" || cat.Rejected[0].PackID != "acme/custom" {
		t.Fatalf("rejected = %+v", cat.Rejected)
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

type stubTemplateLoader struct{}

func (stubTemplateLoader) Abs(_, name string) string { return name }
func (stubTemplateLoader) Get(_ string) (io.Reader, error) {
	return strings.NewReader(""), nil
}

func TestShippedUnitsHaveValidAttachesAndNoWholeBodyReaderFactGuard(t *testing.T) {
	eff, err := extpacks.CatalogForConsumers()
	if err != nil {
		t.Fatalf("CatalogForConsumers: %v", err)
	}
	cat, err := Load(eff)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cat.Rejected) != 0 {
		t.Fatalf("shipped units rejected: %+v", cat.Rejected)
	}
	allowed, err := knownAttachTargets()
	if err != nil {
		t.Fatalf("knownAttachTargets: %v", err)
	}
	units := cat.Units()
	if len(units) == 0 {
		t.Fatal("no shipped units loaded")
	}
	for _, u := range units {
		for _, attach := range u.Attaches {
			if !allowed[attach] {
				t.Errorf("unit %s declares unknown attach %q", u.ID, attach)
			}
		}
		eu, ok := eff.Loaded[u.UnitID]
		if !ok {
			t.Fatalf("missing effective unit %s", u.UnitID)
		}
		if guarded, vars := wholeBodyReaderFactGuard(t, string(Body(eu.Content))); guarded {
			t.Errorf("unit %s wraps its whole body in a reader-fact conditional (%v)", u.ID, vars)
		}
	}

	if guarded, vars := wholeBodyReaderFactGuard(t, "{% if profile_command_mutation != false or profile_has_restore_version %}\n### Editing\n{% endif %}\n"); !guarded || len(vars) != 2 {
		t.Fatalf("expected whole-body reader-fact guard on synthetic unit, got guarded=%v vars=%v", guarded, vars)
	}
}

func wholeBodyReaderFactGuard(t *testing.T, body string) (bool, []string) {
	t.Helper()
	set, err := pongoplain.NewSet("unit-check", stubTemplateLoader{}, pongoplain.Composed)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	tpl, err := set.FromString(body)
	if err != nil {
		t.Fatalf("FromString: %v", err)
	}
	root := reflect.ValueOf(tpl).Elem().FieldByName("root").Elem()
	nodes := root.FieldByName("Nodes")
	var meaningful []reflect.Value
	for i := 0; i < nodes.Len(); i++ {
		node := derefValue(nodes.Index(i))
		if !node.IsValid() {
			continue
		}
		switch node.Type().Name() {
		case "nodeComment":
			continue
		case "nodeHTML":
			val := node.FieldByName("token").Elem().FieldByName("Val").String()
			if strings.TrimSpace(val) == "" {
				continue
			}
		}
		meaningful = append(meaningful, node)
	}
	if len(meaningful) != 1 || meaningful[0].Type().Name() != "tagIfNode" {
		return false, nil
	}
	conditions := meaningful[0].FieldByName("conditions")
	wrappers := meaningful[0].FieldByName("wrappers")
	if conditions.Len() != 1 || wrappers.Len() != 1 {
		return false, nil
	}
	var vars []string
	collectVariableResolvers(conditions.Index(0), &vars)
	if len(vars) == 0 {
		return false, nil
	}
	for _, name := range vars {
		if !isReaderFactIdentifier(name) {
			return false, vars
		}
	}
	return true, vars
}

func derefValue(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func collectVariableResolvers(v reflect.Value, out *[]string) {
	v = derefValue(v)
	if !v.IsValid() {
		return
	}
	if v.Kind() == reflect.Struct && v.Type().Name() == "variableResolver" {
		parts := v.FieldByName("parts")
		if parts.Len() > 0 {
			part := derefValue(parts.Index(0))
			if part.IsValid() {
				if s := part.FieldByName("s"); s.IsValid() && s.Kind() == reflect.String && s.String() != "" {
					*out = append(*out, s.String())
				}
			}
		}
		return
	}
	if v.Kind() == reflect.Struct {
		for i := 0; i < v.NumField(); i++ {
			collectVariableResolvers(v.Field(i), out)
		}
	} else if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		for i := 0; i < v.Len(); i++ {
			collectVariableResolvers(v.Index(i), out)
		}
	}
}

func isReaderFactIdentifier(name string) bool {
	for _, prefix := range []string{
		"profile_",
		"visual_show_",
		"mints",
		"receipt_kind",
		"tool_check_",
		"runner_",
		"agent_has_skill_",
		"caps",
		"surface_",
		"coordinator_",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "web_search_enabled", "recall_may_widen", "progress_gated_tools", "root_count", "execution_mode", "has_file_tools":
		return true
	}
	return false
}

var _ = (*pongo2.Template)(nil)
