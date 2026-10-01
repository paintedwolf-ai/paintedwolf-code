package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const denConsequenceCopyRel = "lycaon-den/src/settings/security/approvals-copy.ts"

// consequenceSentences pulls the high-risk consequence strings Den renders from
// ConsequenceCode. They are Den's to own: the band is presentation, and the
// sentence is chrome keyed by a wire enum rather than anything the host computes.
func consequenceSentences(t *testing.T, root string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, denConsequenceCopyRel))
	testutil.FailErr(t, "read "+denConsequenceCopyRel, err)
	block := string(body)
	start := strings.Index(block, "consequence: {")
	if start < 0 {
		t.Fatalf("%s no longer declares a consequence table", denConsequenceCopyRel)
	}
	// Only the long prose members; the detection entry is a template function.
	literal := regexp.MustCompile(`"([^"\\]{40,})"`)
	var out []string
	for _, m := range literal.FindAllStringSubmatch(block[start:], -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s consequence table has no sentences to compare", denConsequenceCopyRel)
	}
	return out
}

func TestCapabilityCopyDoesNotRestateConsequence(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	sentences := consequenceSentences(t, root)

	hostCopy := map[string]string{
		"hitl.SocketIfWrong":   hitl.SocketIfWrong,
		"hitl.SocketWhatOne":   hitl.SocketWhatOne,
		"hitl.DirectIPWhat":    hitl.DirectIPWhat,
		"hitl.DirectIPIfWrong": hitl.DirectIPIfWrong,
	}
	for name, value := range hostCopy {
		for _, sentence := range sentences {
			if strings.Contains(value, sentence) || strings.Contains(sentence, value) {
				t.Errorf("%s restates a Den consequence sentence; the card would print it twice:\n  %s", name, value)
			}
		}
	}
}

func TestCapabilityCopyHasOneHome(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	canonicalSentences := []string{
		hitl.SocketIfWrong, hitl.DirectIPWhat, hitl.DirectIPIfWrong, hitl.DirectIPAllowLine,
		hitl.SocketAllowLine, hitl.WhoAgentCommand,
	}
	walkGoSources(t, filepath.Join(root, "lycaon"), func(rel, src string) {
		if strings.HasSuffix(rel, "_test.go") || rel == "lycaon/internal/hitl/approval_copy.go" {
			return
		}
		for _, sentence := range canonicalSentences {
			if strings.Contains(src, `"`+sentence+`"`) {
				t.Errorf("%s inlines capability copy that lives in internal/hitl/approval_copy.go:\n  %s", rel, sentence)
			}
		}
	})
}
