package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestDependabotSeparatesMajorsAndPreservesHolds(t *testing.T) {
	t.Parallel()
	p := &policy{Dependabot: dependabotConfig{Interval: "weekly", CooldownDays: 3}}
	sections := []section{{
		cfg: &sectionConfig{Manifest: &manifestConfig{
			Kind: manifestCargo, Path: "engine/Cargo.toml", DependabotGroup: "engine-dependencies",
		}},
		rows: []row{{dependency: "held", judgement: judgement{Updates: updatesHold}}},
	}}
	var document struct {
		Updates []struct {
			Directories []string `yaml:"directories"`
			Groups      map[string]struct {
				AppliesTo   string   `yaml:"applies-to"`
				Patterns    []string `yaml:"patterns"`
				UpdateTypes []string `yaml:"update-types"`
			} `yaml:"groups"`
			Ignore []struct {
				Name string `yaml:"dependency-name"`
			} `yaml:"ignore"`
		} `yaml:"updates"`
	}
	testutil.FailErr(t, "decode generated Dependabot config", yaml.Unmarshal(renderDependabot(p, sections), &document))
	if len(document.Updates) != 1 || !reflect.DeepEqual(document.Updates[0].Directories, []string{"/engine"}) {
		t.Fatalf("unexpected ecosystem entries: %+v", document.Updates)
	}
	u := document.Updates[0]
	g := u.Groups["engine-dependencies"]
	if g.AppliesTo != "version-updates" || !reflect.DeepEqual(g.Patterns, []string{"*"}) ||
		!reflect.DeepEqual(g.UpdateTypes, []string{"minor", "patch"}) {
		t.Errorf("routine group includes unintended updates: %+v", g)
	}
	if len(u.Ignore) != 1 || u.Ignore[0].Name != "held" {
		t.Errorf("held dependency missing: %+v", u.Ignore)
	}
}

func TestInventoryFocusesOnUpdatesAndPreservesSourceAccess(t *testing.T) {
	t.Parallel()
	p := &policy{Title: "Dependency inventory", intro: "Review priorities.", ratings: "Rating guide.", runbooks: "Release procedure."}
	sections := []section{{
		cfg: &sectionConfig{Title: "Packages", file: "sections/packages.yaml", Manifest: &manifestConfig{
			Path: "app/package.json", Lock: "app/bun.lock",
		}},
		rows: []row{
			{name: "urgent", title: "urgent", pins: []value{{text: "1.0.0"}}, upstream: []upstreamRef{{key: "urgent"}},
				judgement: judgement{Urgency: "critical", Friction: "low"}},
			{name: "routine", title: "routine", judgement: judgement{Urgency: "low", Friction: "low"}},
			{name: "unrated"},
			{name: "held", dependency: "held", judgement: judgement{Updates: updatesHold, Notes: "Rebase patch first."}},
			{name: "restricted", dependency: "restricted", judgement: judgement{Updates: updatesNoMajor}},
			{name: "patches", dependency: "patches", judgement: judgement{Updates: updatesPatchOnly}},
			{name: "difficult", judgement: judgement{Urgency: "low", Friction: "high", Notes: "Verify native behavior."}},
		},
	}, {
		cfg: &sectionConfig{Title: "Tools", file: "sections/tools.yaml"},
	}, {
		cfg: &sectionConfig{Title: "Go modules", file: "sections/go.yaml", Manifest: &manifestConfig{Path: "host/go.mod"}},
	}}
	snap := snapshot{Fetched: "2026-10-07", Versions: map[string]string{"urgent": "1.1.0"}}
	body := string(renderInventory(p, sections, snap))
	for _, want := range []string{
		"**urgent** (critical): `1.0.0` → `1.1.0`", "held", "Held", "Rebase patch first.",
		"restricted", "No majors", "patches", "Patches only", "difficult", "Verify native behavior.",
		"| Packages | 7 | [Policy](../../dependencies/sections/packages.yaml)",
		"[Manifest](../../app/package.json) · [Lockfile](../../app/bun.lock)",
		"| Tools | 0 | [Policy](../../dependencies/sections/tools.yaml) | Declared pin sources in policy |",
		"[Manifest](../../host/go.mod)", "Rating guide.", "Release procedure.", "**2026-10-07**",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("inventory lost review information %q", want)
		}
	}
	for _, unwanted := range []string{"routine", "unrated", "| Members |"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("inventory repeats non-actionable detail %q", unwanted)
		}
	}
}

func TestInventoryWithoutPriorityUpdatesOrConstraints(t *testing.T) {
	t.Parallel()
	body := string(renderInventory(&policy{Title: "Inventory"}, nil, snapshot{}))
	for _, want := range []string{"None as of the snapshot.", "None declared.", "## Complete inventory sources"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty inventory missing %q", want)
		}
	}
	if strings.Contains(body, "| Name |") {
		t.Error("empty inventory rendered a constraint table")
	}
}
