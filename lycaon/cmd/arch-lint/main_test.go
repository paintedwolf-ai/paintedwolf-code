package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/archlint"
	"github.com/lycaon/lycaon/internal/testutil"
)

func decode(t *testing.T, out *bytes.Buffer) []archlint.Finding {
	t.Helper()
	var got []archlint.Finding
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", out.String(), err)
	}
	return got
}

func TestSubcommandsWriteFindingsAsJSON(t *testing.T) {
	fixtures := filepath.Join("..", "..", "internal", "archlint")
	var out bytes.Buffer
	if err := runVet([]string{"-dir", filepath.Join(fixtures, "testdata", "vetmod"), "-test-support", "test$", "./..."}, &out); err != nil {
		testutil.FailErr(t, "runVet failed", err)
	}
	if got := decode(t, &out); len(got) != 1 || got[0].Kind != "reexport" {
		t.Fatalf("vet = %+v", got)
	}
	out.Reset()
	if err := runReach([]string{
		"-dir", filepath.Join(fixtures, "reach", "testdata", "fixture"), "-goos", "linux",
		"-report", "^example.com/fixture/", "-skip", "test$", "./cmd/...",
	}, &out); err != nil {
		t.Fatal(err)
	}
	if got := decode(t, &out); len(got) == 0 {
		t.Fatal("reach found nothing in a fixture with dead code")
	}
	out.Reset()
	indexFile := filepath.Join(t.TempDir(), "index")
	testutil.FailErr(t, "write index", os.WriteFile(indexFile, []byte("100755 0000000000000000000000000000000000000000 0\tscripts/orphan.sh\x00"), 0o600))
	if err := runRefs([]string{"-root", t.TempDir(), "-index", indexFile}, strings.NewReader(""), &out); err != nil {
		testutil.FailErr(t, "runRefs failed", err)
	}
	if got := decode(t, &out); len(got) != 1 || got[0].Key != "scripts/orphan.sh" {
		t.Fatalf("refs = %+v", got)
	}
}

func TestSubcommandsRefuseBadFlags(t *testing.T) {
	var out bytes.Buffer
	for name, run := range map[string]func([]string, *bytes.Buffer) error{
		"reach": func(a []string, o *bytes.Buffer) error { return runReach(a, o) },
		"vet":   func(a []string, o *bytes.Buffer) error { return runVet(a, o) },
		"refs":  func(a []string, o *bytes.Buffer) error { return runRefs(a, strings.NewReader(""), o) },
	} {
		if err := run([]string{"-nope"}, &out); err == nil {
			t.Errorf("%s accepted an unknown flag", name)
		}
	}
	if err := runReach([]string{"-report", "(", "./..."}, &out); err == nil {
		t.Error("reach accepted an invalid report pattern")
	}
	if err := runReach([]string{"-skip", "(", "./..."}, &out); err == nil {
		t.Error("reach accepted an invalid skip pattern")
	}
	if err := runRefs(nil, strings.NewReader(""), &out); err == nil {
		t.Error("refs ran without an index")
	}
	if err := runRefs([]string{"-index", filepath.Join(t.TempDir(), "missing")}, strings.NewReader(""), &out); err == nil {
		t.Error("refs read a missing index file")
	}
	if err := runRefs([]string{"-index", "-"}, strings.NewReader("not an index entry\x00"), &out); err == nil {
		t.Error("refs accepted an index that is not ls-files output")
	}
	if err := runVet([]string{"-test-support", "(", "./..."}, &out); err == nil {
		t.Error("vet accepted an invalid test-support pattern")
	}
	if err := runVet([]string{"-dir", filepath.Join(t.TempDir(), "missing"), "./..."}, &out); err == nil {
		t.Error("vet loaded a missing module")
	}
	if err := runReach([]string{"-dir", filepath.Join("..", "..", "internal", "archlint", "reach", "testdata", "fixture"), "-goos", "linux", "./store"}, &out); err == nil {
		t.Error("reach judged a load with no main package")
	}
	if got := splitList(" a, ,b "); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitList = %q", got)
	}
}
