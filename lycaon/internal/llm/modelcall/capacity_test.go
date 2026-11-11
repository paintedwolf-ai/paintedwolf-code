package modelcall

import "testing"

type capacityProvider struct {
	Provider
	id    string
	group *CapacityGroup
}

func (p capacityProvider) ID() string                    { return p.id }
func (p capacityProvider) CapacityGroup() *CapacityGroup { return p.group }

type independentProvider struct {
	Provider
	id string
}

func (p independentProvider) ID() string { return p.id }

func TestCapacityKey(t *testing.T) {
	group := NewCapacityGroup()
	first := capacityProvider{id: "coordinator", group: group}
	alias := capacityProvider{id: "utility", group: group}
	if CapacityKey(first) != CapacityKey(alias) {
		t.Fatal("runtime aliases have separate capacity")
	}
	other := capacityProvider{id: first.id, group: NewCapacityGroup()}
	if CapacityKey(first) == CapacityKey(other) {
		t.Fatal("independent runtimes share capacity")
	}
	for _, group := range []*CapacityGroup{nil, {}} {
		fallback := capacityProvider{id: "remote", group: group}
		if CapacityKey(fallback) != CapacityKey(independentProvider{id: "remote"}) {
			t.Fatal("missing runtime identity lost provider fallback")
		}
	}
	if CapacityKey(independentProvider{id: "one"}) == CapacityKey(independentProvider{id: "two"}) {
		t.Fatal("independent provider IDs share capacity")
	}
}
