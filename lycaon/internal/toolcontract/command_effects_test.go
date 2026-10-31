package toolcontract

import "testing"

func TestCatalogEffectModesHaveClosedAdmissionClasses(t *testing.T) {
	catalog, err := LanguageV1()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	child, err := NewCommandEffects(CommandReadOnly)
	if err != nil {
		t.Fatalf("compile child effect set: %v", err)
	}
	for _, command := range catalog.Commands {
		for _, mode := range command.Effects {
			if _, err := mode.CommandEffects(&child); err != nil {
				t.Errorf("%s/%s: %v", command.Name, mode.Mode, err)
			}
		}
	}
}

func TestCommandEffectAdmissionRequiresEveryClassAndBoundChildren(t *testing.T) {
	readonly, _ := NewCommandEffects(CommandReadOnly)
	mutation, _ := NewCommandEffects(CommandMutation)
	all, _ := NewCommandEffects(CommandReadOnly, CommandMutation, CommandExternalExec, CommandNetworkEgress)
	writeNetwork, err := (EffectRequirement{Mode: "request", Requires: []string{"write", "egress", "review"}}).CommandEffects(nil)
	if err != nil {
		t.Fatalf("compile write+egress: %v", err)
	}
	if readonly.Allows(writeNetwork) || mutation.Allows(writeNetwork) || !all.Allows(writeNetwork) {
		t.Fatal("set-valued effects admitted by only one class")
	}
	wrapper := EffectRequirement{Mode: "dispatch", Requires: []string{"bound-child-effect-set"}}
	if _, err := wrapper.CommandEffects(nil); err == nil {
		t.Fatal("unbound child admitted")
	}
	for _, invalid := range []CommandEffects{0, CommandEffects(255)} {
		if _, err := wrapper.CommandEffects(&invalid); err == nil {
			t.Fatal("empty or unknown bound child admitted as read-only")
		}
	}
	bound, err := wrapper.CommandEffects(&mutation)
	if err != nil || readonly.Allows(bound) || !all.Allows(bound) {
		t.Fatalf("dynamic mutation effects=%v err=%v", bound.Classes(), err)
	}
	if _, err := (EffectRequirement{Mode: "unknown", Requires: []string{"invented-permission"}}).CommandEffects(nil); err == nil {
		t.Fatal("unknown obligation admitted")
	}
	if readonly.Allows(0) || all.Allows(CommandEffects(255)) {
		t.Fatal("invalid or empty effect set admitted")
	}
	for _, classes := range [][]CommandEffectClass{{"unknown"}, {CommandReadOnly, CommandReadOnly}} {
		if _, err := NewCommandEffects(classes...); err == nil {
			t.Fatalf("invalid classes admitted %v", classes)
		}
	}
	detached := all.Classes()
	detached[0] = "changed"
	if all.Classes()[0] != CommandReadOnly {
		t.Fatal("class projection mutates authority")
	}
}
