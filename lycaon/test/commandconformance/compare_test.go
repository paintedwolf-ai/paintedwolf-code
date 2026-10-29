package commandconformance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

func TestMatchComparesEveryObservableResult(t *testing.T) {
	ref := Snapshot{Status: 1, Stdout: Text("output\n"), Stderr: Text("diagnostic\n"),
		Files: map[string]*File{"result": {Mode: 0o644, Data: Text("bytes\n")}, "gone": nil}}
	cases := []struct {
		name   string
		change func(*Observation)
		want   string
	}{
		{"equal", func(*Observation) {}, ""},
		{"status", func(o *Observation) { o.Status = 0 }, "status 1, now 0"},
		{"stdout", func(o *Observation) { o.Stdout = append(o.Stdout, "hidden"...) }, `stdout "output\n", now "output\nhidden"`},
		{"stderr", func(o *Observation) { o.Stderr = nil }, `stderr "diagnostic\n", now ""`},
		{"file bytes", func(o *Observation) { o.Files["result"] = &File{Mode: 0o644, Data: Text("other\n")} }, `file result -rw-r--r-- "bytes\n", now -rw-r--r-- "other\n"`},
		{"file mode", func(o *Observation) { o.Files["result"] = &File{Mode: 0o600, Data: Text("bytes\n")} }, `file result -rw-r--r-- "bytes\n", now -rw------- "bytes\n"`},
		{"file missing", func(o *Observation) { o.Files["result"] = nil }, `file result -rw-r--r-- "bytes\n" is missing`},
		{"file created", func(o *Observation) { o.Files["gone"] = &File{Mode: 0o644} }, `file gone should not exist, now -rw-r--r-- ""`},
		{"file unobserved", func(o *Observation) { delete(o.Files, "gone") }, "file gone was not observed"},
		{"file untracked", func(o *Observation) { o.Files["extra"] = nil }, "file extra is not tracked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := Observation{Status: 1, Stdout: []byte("output\n"), Stderr: []byte("diagnostic\n"),
				Files: map[string]*File{"result": {Mode: 0o644, Data: Text("bytes\n")}, "gone": nil}}
			tc.change(&obs)
			matched, diffs := Match(obs, map[referenceuserland.ID]Snapshot{referenceuserland.GNU: ref})
			if tc.want == "" {
				if matched != referenceuserland.GNU || diffs != nil {
					t.Fatalf("Match = %q %v, want gnu", matched, diffs)
				}
				return
			}
			if matched != "" || diffs[referenceuserland.GNU].Text != tc.want {
				t.Fatalf("Match = %q %q, want the difference %q", matched, diffs[referenceuserland.GNU].Text, tc.want)
			}
		})
	}
}

func TestMatchAcceptsAnyReferenceAndReportsEachDifference(t *testing.T) {
	refs := map[referenceuserland.ID]Snapshot{
		referenceuserland.GNU: {Stdout: Text(" 1 notes\n")},
		referenceuserland.BSD: {Stdout: Text("       1 notes\n")},
	}
	if matched, _ := Match(Observation{Stdout: []byte("       1 notes\n")}, refs); matched != referenceuserland.BSD {
		t.Fatalf("matched %q, want bsd", matched)
	}
	_, diffs := Match(Observation{Stdout: []byte("1 notes\n")}, refs)
	if got := FormatDiffs(diffs); got != "\n  bsd: stdout \"       1 notes\\n\", now \"1 notes\\n\"\n  gnu: stdout \" 1 notes\\n\", now \"1 notes\\n\"" {
		t.Fatalf("diffs = %s", got)
	}
}

func TestMatchDropsTheReferenceShellsLineNumber(t *testing.T) {
	refs := map[referenceuserland.ID]Snapshot{
		referenceuserland.GNU:     {Status: 127, Stderr: Text("/bin/bash: line 3: tool: command not found\n")},
		referenceuserland.BusyBox: {Status: 127, Stderr: Text("/bin/sh: tool: not found\n")},
	}
	if matched, diffs := Match(Observation{Status: 127, Stderr: []byte("bash: tool: command not found\n")}, refs); matched != referenceuserland.GNU {
		t.Fatalf("shell error did not match gnu: %v", diffs)
	}
	// Only the shell's own prefix is rewritten, not text a program printed.
	if matched, _ := Match(Observation{Status: 127, Stderr: []byte("bash: tool: command not found\n")}, map[referenceuserland.ID]Snapshot{
		referenceuserland.GNU: {Status: 127, Stderr: Text("printed /bin/bash: line 3: tool: command not found\n")},
	}); matched != "" {
		t.Fatal("a line inside program output was treated as a shell error")
	}
}

func TestNormalizeMapsEveryWorkspaceRoot(t *testing.T) {
	obs := Observation{
		Stdout: []byte("/private/var/w/sub\n/var/w/sub\n"),
		Stderr: []byte("cat: /var/w/x: No such file or directory\n"),
		Files:  map[string]*File{"out": {Mode: 0o644, Data: Text("/var/w\n")}, "gone": nil},
	}
	got := obs.Normalize("/var/w", "/private/var/w")
	if string(got.Stdout) != "/workspace/sub\n/workspace/sub\n" || !strings.HasPrefix(string(got.Stderr), "cat: /workspace/x:") ||
		string(got.Files["out"].Data) != "/workspace\n" || got.Files["gone"] != nil {
		t.Fatalf("normalized = %+v", got)
	}
	if string(obs.Files["out"].Data) != "/var/w\n" {
		t.Fatal("Normalize changed the observation it was given")
	}
}
