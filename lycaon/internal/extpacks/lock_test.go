package extpacks

import (
	"reflect"
	"testing"
)

func samplePackage(id, version string) LockedPackage {
	return LockedPackage{
		ID:        id,
		Version:   version,
		Source:    "https://example.com/" + id + ".git",
		Revision:  "rev-" + version,
		Integrity: "sha256:" + version,
		Kind:      PackKindGit,
	}
}

func TestUpsertLockedPackageDoesNotMutateInput(t *testing.T) {
	original := LockFile{Packages: []LockedPackage{
		samplePackage("a", "1.0.0"),
		samplePackage("b", "1.0.0"),
	}}
	before := append([]LockedPackage(nil), original.Packages...)

	_ = upsertLockedPackage(original, samplePackage("a", "2.0.0"))

	for i, pkg := range original.Packages {
		if !reflect.DeepEqual(pkg, before[i]) {
			t.Fatalf("upsertLockedPackage mutated its input: package %d is %+v, want %+v", i, pkg, before[i])
		}
	}
}

func TestUpsertLockedPackageReplacesExisting(t *testing.T) {
	original := LockFile{Packages: []LockedPackage{
		samplePackage("a", "1.0.0"),
		samplePackage("b", "1.0.0"),
	}}

	next := upsertLockedPackage(original, samplePackage("a", "2.0.0"))

	if len(next.Packages) != 2 {
		t.Fatalf("got %d packages, want 2", len(next.Packages))
	}
	pkg, ok := next.Package("a")
	if !ok || pkg.Version != "2.0.0" {
		t.Fatalf("package a is %+v, ok=%v; want version 2.0.0", pkg, ok)
	}
}

func TestUpsertLockedPackageAddsNew(t *testing.T) {
	original := LockFile{Packages: []LockedPackage{samplePackage("a", "1.0.0")}}

	next := upsertLockedPackage(original, samplePackage("c", "1.0.0"))

	if len(next.Packages) != 2 {
		t.Fatalf("got %d packages, want 2", len(next.Packages))
	}
	if _, ok := next.Package("c"); !ok {
		t.Fatalf("package c was not added")
	}
}

func TestDropLockedPackageDoesNotMutateInput(t *testing.T) {
	original := LockFile{Packages: []LockedPackage{
		samplePackage("a", "1.0.0"),
		samplePackage("b", "1.0.0"),
	}}
	before := append([]LockedPackage(nil), original.Packages...)

	_ = dropLockedPackage(original, "a")

	if len(original.Packages) != len(before) {
		t.Fatalf("dropLockedPackage shrank its input: got %d packages, want %d", len(original.Packages), len(before))
	}
	for i, pkg := range original.Packages {
		if !reflect.DeepEqual(pkg, before[i]) {
			t.Fatalf("dropLockedPackage mutated its input: package %d is %+v, want %+v", i, pkg, before[i])
		}
	}
}

func TestDropLockedPackageRemoves(t *testing.T) {
	original := LockFile{Packages: []LockedPackage{
		samplePackage("a", "1.0.0"),
		samplePackage("b", "1.0.0"),
	}}

	next := dropLockedPackage(original, "a")

	if len(next.Packages) != 1 {
		t.Fatalf("got %d packages, want 1", len(next.Packages))
	}
	if _, ok := next.Package("a"); ok {
		t.Fatalf("package a was not removed")
	}
	if _, ok := next.Package("b"); !ok {
		t.Fatalf("package b was unexpectedly removed")
	}
}

func TestEncodeLockDoesNotMutateInput(t *testing.T) {
	original := LockFile{
		LockFormat: LockFormat,
		Packages: []LockedPackage{
			samplePackage("b", "1.0.0"),
			samplePackage("a", "1.0.0"),
		},
	}
	before := append([]LockedPackage(nil), original.Packages...)

	if _, err := EncodeLock(original); err != nil {
		t.Fatalf("EncodeLock: %v", err)
	}

	for i, pkg := range original.Packages {
		if !reflect.DeepEqual(pkg, before[i]) {
			t.Fatalf("EncodeLock mutated its input: package %d is %+v, want %+v (normalizeLock/sort leaked into the caller's slice)", i, pkg, before[i])
		}
	}
}
