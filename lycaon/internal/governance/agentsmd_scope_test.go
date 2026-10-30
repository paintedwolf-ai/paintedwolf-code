package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolvedPolicyIdentityIncludesExactSourceAndWorkspace(t *testing.T) {
	root := t.TempDir()
	body := "Root instructions\n"
	testutil.FailErr(t, "write root policy", os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(body), 0o600))
	files, err := ResolveScopes(t.Context(), []agentpolicy.Scope{
		{RootID: "root", RootPath: root, Path: "one/new.go"},
		{RootID: "root", RootPath: root, Path: "two/new.go"},
		{RootID: "root", RootPath: root, Path: "."},
	}, DefaultAgentsMDInjectMaxBodyBytes)
	testutil.FailErr(t, "resolve shared ancestors", err)
	if len(files) != 1 || files[0].Content != body || files[0].Revision.RootID != "root" || files[0].Revision.Path != "AGENTS.md" {
		t.Fatalf("resolved policy = %+v", files)
	}
	digest := sha256.Sum256([]byte(body))
	if files[0].Revision.Digest != hex.EncodeToString(digest[:]) {
		t.Fatal("policy revision did not identify exact source bytes")
	}
	copyRoot := t.TempDir()
	testutil.FailErr(t, "write copied policy", os.WriteFile(filepath.Join(copyRoot, "AGENTS.md"), []byte(body), 0o600))
	copy, err := ResolveScopes(t.Context(), []agentpolicy.Scope{{RootID: "root", RootPath: copyRoot, Path: "."}}, DefaultAgentsMDInjectMaxBodyBytes)
	testutil.FailErr(t, "resolve copied workspace", err)
	if files[0].Revision == copy[0].Revision || files[0].Revision.Digest != copy[0].Revision.Digest {
		t.Fatal("physical workspace identity was collapsed into content identity")
	}
}

func TestResolvedPolicyRejectsUnresolvedOrEscapingScopes(t *testing.T) {
	root := t.TempDir()
	for _, scope := range []agentpolicy.Scope{
		{RootPath: root, Path: "."},
		{RootID: "root", RootPath: "relative", Path: "."},
		{RootID: "root", RootPath: root, Path: filepath.Join(root, "file.go")},
		{RootID: "root", RootPath: root, Path: "../file.go"},
		{RootID: "root", RootPath: root, Path: "file\x00.go"},
	} {
		if _, err := ResolveScopes(t.Context(), []agentpolicy.Scope{scope}, DefaultAgentsMDInjectMaxBodyBytes); err == nil {
			t.Fatalf("accepted unresolved policy scope: %+v", scope)
		}
	}
}
