package git

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommitStatusPreservesPathsAndStructuredStates(t *testing.T) {
	raw := strings.Join([]string{
		"# branch.oid abc123", "# branch.head main",
		"1 .M N... 100644 100644 100644 aaa aaa  leading and trailing ",
		"2 R. N... 100644 100644 100644 aaa aaa R100 new\nname", "old\tname",
		"u UU N... 100644 100644 100644 100644 aaa bbb ccc conflict",
		"1 .M S.MU 160000 160000 160000 aaa aaa submodule",
		"? :(glob)*.txt", "",
	}, "\x00")
	status, err := parseCommitStatus([]byte(raw))
	testutil.FailErr(t, "parse status", err)
	if status.Head != "abc123" || len(status.Files) != 5 {
		t.Fatalf("status=%+v", status)
	}
	if status.Files[0].Path != " leading and trailing " || status.Files[1].Path != "new\nname" || status.Files[1].FromPath != "old\tname" {
		t.Fatalf("paths changed: %+v", status.Files)
	}
	if status.Files[2].Status != "UU" || status.Files[3].Submodule != "S.MU" || status.Files[4].Path != ":(glob)*.txt" {
		t.Fatalf("facts changed: %+v", status.Files)
	}
}

func TestCommitStatusRejectsIncompleteProtocol(t *testing.T) {
	for _, raw := range []string{"? file\x00", "# branch.oid abc\x00x unknown\x00", "# branch.oid abc\x002 R. N... 100644 100644 100644 a a R100 dest\x00"} {
		if _, err := parseCommitStatus([]byte(raw)); err == nil {
			t.Fatalf("accepted incomplete status %q", raw)
		}
	}
	status, err := parseCommitStatus([]byte("# branch.oid (initial)\x00? new\x00"))
	testutil.FailErr(t, "parse unborn", err)
	if status.Head != "" || len(status.Files) != 1 {
		t.Fatalf("unborn=%+v", status)
	}
}

func parseCommitStatus(raw []byte) (CommitStatus, error) {
	var p commitStatusParser
	for _, record := range strings.Split(string(raw), "\x00") {
		if err := p.record([]byte(record)); err != nil {
			return CommitStatus{}, err
		}
	}
	return p.finish()
}
