package main

import (
	"reflect"
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
