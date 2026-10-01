package skills

import (
	"reflect"
	"testing"
)

func TestSelectorSelect(t *testing.T) {
	t.Parallel()
	catalog := []Skill{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}

	if got := (Selector{All: true}).Select(catalog); !reflect.DeepEqual(got, catalog) {
		t.Fatalf("all = %#v, want %#v", got, catalog)
	}
	got := (Selector{Names: []string{"gamma", "alpha"}}).Select(catalog)
	want := []Skill{{Name: "alpha"}, {Name: "gamma"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("exact = %#v, want %#v", got, want)
	}
	if got := (Selector{}).Select(catalog); len(got) != 0 {
		t.Fatalf("omitted selector = %#v, want none", got)
	}
}

func TestSelectorEmpty(t *testing.T) {
	t.Parallel()
	if !(Selector{}).Empty() {
		t.Fatal("zero selector must be empty")
	}
	if (Selector{All: true}).Empty() || (Selector{Names: []string{"alpha"}}).Empty() {
		t.Fatal("configured selector must not be empty")
	}
}
