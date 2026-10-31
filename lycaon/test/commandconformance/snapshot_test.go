package commandconformance

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/test/referenceuserland"
)

func TestTextReadsAsAStringUnlessItIsNotUTF8(t *testing.T) {
	for _, tc := range []struct {
		text Text
		json string
	}{
		{Text("a <b> & c\n"), `"a <b> & c\n"`},
		{Text{0xff, 0x00, 'x'}, `{"base64":"/wB4"}`},
	} {
		data, err := encodeJSON(tc.text, "")
		testutil.FailErr(t, "marshal text", err)
		if string(data) != tc.json {
			t.Fatalf("marshal %q = %s, want %s", tc.text, data, tc.json)
		}
		var back Text
		testutil.FailErr(t, "unmarshal text", json.Unmarshal(data, &back))
		if string(back) != string(tc.text) {
			t.Fatalf("round trip %q = %q", tc.text, back)
		}
	}
	var bad Text
	if err := json.Unmarshal([]byte(`7`), &bad); err == nil {
		t.Fatal("a number decoded as text")
	}
}

func TestModeReadsAsTheListingString(t *testing.T) {
	for _, mode := range []fs.FileMode{0o644, 0o600, 0o755 | fs.ModeDir, 0o777 | fs.ModeSymlink} {
		data, err := json.Marshal(Mode(mode))
		testutil.FailErr(t, "marshal mode", err)
		if string(data) != `"`+mode.String()+`"` {
			t.Fatalf("marshal %v = %s", mode, data)
		}
		var back Mode
		testutil.FailErr(t, "unmarshal mode", json.Unmarshal(data, &back))
		if fs.FileMode(back) != mode {
			t.Fatalf("round trip %v = %v", mode, fs.FileMode(back))
		}
	}
	for _, bad := range []string{`"rw-r--r--"`, `"prw-r--r--"`, `"-rwSr--r--"`, `7`} {
		var m Mode
		if err := json.Unmarshal([]byte(bad), &m); err == nil {
			t.Errorf("mode %s decoded as %v", bad, fs.FileMode(m))
		}
	}
}

func TestSnapshotsRefuseMissingOrStaleResults(t *testing.T) {
	c := Case{ID: "c", Program: "true", References: bashUserlands}
	fresh := Snapshot{Input: c.Input(referenceuserland.GNU, nil)}
	s := &Snapshots{Cases: map[string]map[referenceuserland.ID]Snapshot{"c": {referenceuserland.GNU: fresh}}}
	if _, err := s.For(c, nil); err == nil || !strings.Contains(err.Error(), "the bsd snapshot of case c is missing or stale") {
		t.Fatalf("missing bsd snapshot: %v", err)
	}
	s.Cases["c"][referenceuserland.BSD] = Snapshot{Input: c.Input(referenceuserland.BSD, nil)}
	refs, err := s.For(c, nil)
	testutil.FailErr(t, "fresh snapshots", err)
	if len(refs) != 2 {
		t.Fatalf("refs = %v", refs)
	}
	changed := c
	changed.Program = "false"
	if _, err := s.For(changed, nil); err == nil {
		t.Fatal("a snapshot of another program was accepted")
	}
}

func TestInputCoversEverythingTheReferenceRunDependsOn(t *testing.T) {
	seed := GitSeed{".git/HEAD": {Data: []byte("ref: refs/heads/main\n")}}
	base := Case{ID: "c", Program: "git status", Tracked: []string{"a"}, Git: true}
	inputs := map[string]bool{base.Input(referenceuserland.GNU, seed): true}
	for _, other := range []string{
		base.Input(referenceuserland.BusyBox, seed),
		base.Input(referenceuserland.GNU, GitSeed{".git/HEAD": {Data: []byte("other\n")}}),
		Case{ID: "c", Program: "git log", Tracked: []string{"a"}, Git: true}.Input(referenceuserland.GNU, seed),
		Case{ID: "c", Program: "git status", Git: true}.Input(referenceuserland.GNU, seed),
		Case{ID: "c", Program: "git status", Tracked: []string{"a"}}.Input(referenceuserland.GNU, seed),
	} {
		if inputs[other] {
			t.Fatalf("input %s repeats for a different reference run", other)
		}
		inputs[other] = true
	}
	firstInput := base.Input(referenceuserland.GNU, seed)
	secondInput := base.Input(referenceuserland.GNU, seed)
	if firstInput != secondInput {
		t.Fatal("input is not stable")
	}
}

func TestRetainKeepsOnlyWhatTheCorpusCompares(t *testing.T) {
	s := &Snapshots{Cases: map[string]map[referenceuserland.ID]Snapshot{
		"kept":    {referenceuserland.GNU: {}, referenceuserland.BusyBox: {}},
		"removed": {referenceuserland.GNU: {}},
	}}
	s.Retain([]Case{{ID: "kept", References: gnuUserland}})
	if len(s.Cases) != 1 || len(s.Cases["kept"]) != 1 {
		t.Fatalf("retained %v", s.Cases)
	}
}

func TestLoadSnapshotsRefusesResultsFromOtherImages(t *testing.T) {
	stored, err := LoadSnapshots()
	testutil.FailErr(t, "load the stored snapshots", err)
	manifest, err := referenceuserland.LoadManifest()
	testutil.FailErr(t, "load the manifest", err)
	if !imagesEqual(stored.Userlands, Images(manifest)) {
		t.Fatal("loaded snapshots from other images")
	}
	other := Images(manifest)
	other[referenceuserland.GNU] = Image{Base: other[referenceuserland.GNU].Base, Trees: map[string]string{"arm64": "sha256:other"}}
	if imagesEqual(stored.Userlands, other) {
		t.Fatal("a different tree was taken for the same image")
	}
	if _, err := decodeSnapshots([]byte(`{"cases": []}`)); err == nil {
		t.Fatal("malformed snapshots decoded")
	}
	empty, err := decodeSnapshots([]byte(`{}`))
	testutil.FailErr(t, "decode empty snapshots", err)
	if empty.Cases == nil {
		t.Fatal("decoded snapshots cannot take new cases")
	}
}
