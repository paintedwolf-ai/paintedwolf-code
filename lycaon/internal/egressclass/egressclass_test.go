package egressclass

import (
	"reflect"
	"testing"
)

func TestEgressClassesAreWellFormed(t *testing.T) {
	t.Parallel()
	classes := All()
	if len(classes) == 0 {
		t.Fatal("egress inventory is empty")
	}
	for _, c := range classes {
		switch c.Transport {
		case HTTPBounded, HTTPStreaming, HTTPDownload, GitCLI:
		default:
			t.Errorf("%s has unknown transport %q", c.ID, c.Transport)
		}
		switch c.EndpointSource {
		case EndpointBundled, EndpointUserConfigured, EndpointMixed:
		default:
			t.Errorf("%s has unknown endpoint source %q", c.ID, c.EndpointSource)
		}
		if c.EndpointSource != EndpointBundled && (len(c.FixedEndpoints) != 0 || c.EndpointTemplate != "") {
			t.Errorf("%s is configured but declares fixed endpoints %q", c.ID, c.FixedEndpoints)
		}
	}
}

func TestEgressClassLookup(t *testing.T) {
	t.Parallel()
	for _, c := range All() {
		got, ok := Lookup(c.ID)
		if !ok {
			t.Fatalf("Lookup(%q) not found", c.ID)
		}
		if !reflect.DeepEqual(got, c) {
			t.Fatalf("Lookup(%q) = %+v, want %+v", c.ID, got, c)
		}
	}
	if _, ok := Lookup("unknown"); ok {
		t.Fatal("Lookup returned a class for an id that is not in the inventory")
	}
}

func TestEgressClassInventoryCopiesNestedEndpointState(t *testing.T) {
	t.Parallel()
	const mutated = "https://mutated.invalid"
	first := All()
	looked, ok := Lookup(OSVAdvisoryDownload)
	if !ok || len(looked.FixedEndpoints) == 0 {
		t.Fatalf("Lookup(%q) = %+v, want a class with fixed endpoints", OSVAdvisoryDownload, looked)
	}
	looked.FixedEndpoints[0] = mutated
	touched := 0
	for index := range first {
		if len(first[index].FixedEndpoints) > 0 {
			first[index].FixedEndpoints[0] = mutated
			touched++
		}
	}
	if touched == 0 {
		t.Fatal("no class declares fixed endpoints; the copy is untested")
	}
	for _, class := range All() {
		for _, endpoint := range class.FixedEndpoints {
			if endpoint == mutated {
				t.Fatalf("%s: a caller's edit reached the inventory", class.ID)
			}
		}
	}
}

// Each class declares a failure notice or an explicit silent disposition.
func TestEgressFailureCodesAreWellFormed(t *testing.T) {
	t.Parallel()
	notices := 0
	for _, c := range All() {
		if c.NoticeCode == "" {
			if c.SilentReason == "" {
				t.Errorf("%s declares no notice code and no silent reason", c.ID)
			}
			continue
		}
		notices++
		if c.SilentReason != "" {
			t.Errorf("%s emits %q and also declares a silent reason", c.ID, c.NoticeCode)
		}
		for _, r := range c.NoticeCode {
			if (r < 'a' || r > 'z') && r != '_' {
				t.Errorf("%s notice code %q is not snake_case", c.ID, c.NoticeCode)
				break
			}
		}
	}
	if notices == 0 {
		t.Fatal("no class declares a notice code")
	}
}

func TestEgressFailureCodesAreDistinct(t *testing.T) {
	t.Parallel()
	seen := map[string]ID{}
	for _, c := range All() {
		if c.NoticeCode == "" {
			continue
		}
		if prev, dup := seen[c.NoticeCode]; dup {
			t.Fatalf("classes %s and %s share notice code %q", prev, c.ID, c.NoticeCode)
		}
		seen[c.NoticeCode] = c.ID
	}
	if len(seen) < 2 {
		t.Fatalf("%d notice codes; distinctness needs at least two", len(seen))
	}
}
