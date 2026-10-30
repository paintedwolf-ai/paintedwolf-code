package agentpolicy

import "testing"

func TestManifestRequiresExactPolicyIdentity(t *testing.T) {
	file := Revision{RootID: "root", RootPath: "/workspace", Path: "AGENTS.md", Digest: "bytes"}
	delivered := Manifest{Files: []Revision{file}}
	if !delivered.Covers(Manifest{Files: []Revision{file, file}}) || !delivered.Covers(Manifest{}) {
		t.Fatal("delivered policy did not cover its current revision")
	}
	for _, change := range []func(*Revision){
		func(r *Revision) { r.RootID = "other" },
		func(r *Revision) { r.RootPath = "/worktree" },
		func(r *Revision) { r.Path = "nested/AGENTS.md" },
		func(r *Revision) { r.Digest = "new bytes" },
	} {
		changed := file
		change(&changed)
		if delivered.Covers(Manifest{Files: []Revision{changed}}) {
			t.Fatalf("covered another policy identity: %+v", changed)
		}
	}
	copy := delivered.Clone()
	copy.Files[0].Digest = "changed copy"
	if delivered.Files[0] != file {
		t.Fatal("manifest clone shared mutable storage")
	}
}
