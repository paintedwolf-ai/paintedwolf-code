package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

const evidenceBoundarySecret = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"

// evidenceScreenRedactor stands in for the durable secret screen.
func evidenceScreenRedactor(_ context.Context, msg api.Message) (api.Message, bool) {
	out := msg
	out.Content = strings.ReplaceAll(msg.Content, evidenceBoundarySecret, "{{paintedwolf-secret:ci_token}}")
	return out, out.Content != msg.Content
}

// evidenceFileBytes reads every verify evidence row this project wrote.
func evidenceFileBytes(t *testing.T, m *Manager, sess *api.Session) []byte {
	t.Helper()
	root := m.evidenceRootFor(sess)
	if strings.TrimSpace(root) == "" {
		t.Fatal("evidence root is unset")
	}
	var all []byte
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		all = append(all, data...)
		return nil
	})
	testutil.FailErr(t, "walk evidence dir", err)
	if len(all) == 0 {
		t.Fatal("no evidence rows were written")
	}
	return all
}

func TestSourceRunEvidenceIsScreenedBeforeItIsPersisted(t *testing.T) {
	mgr, sess, _ := verifyGateHarness(t, "")
	mgr.SetMessageStorageRedactor(evidenceScreenRedactor)
	recordVerify(t, mgr, sess, "deploy --token "+evidenceBoundarySecret, 0)

	rows := evidenceFileBytes(t, mgr, sess)
	if strings.Contains(string(rows), evidenceBoundarySecret) {
		t.Fatalf("raw credential reached the evidence file: %s", rows)
	}
	if !strings.Contains(string(rows), "{{paintedwolf-secret:ci_token}}") {
		t.Fatalf("evidence row did not keep the reference-bearing command: %s", rows)
	}
}

func TestScreenedEvidenceStillMatchesTheDeclaredCommand(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "make test")
	mgr.SetMessageStorageRedactor(evidenceScreenRedactor)
	recordVerify(t, mgr, sess, "make test", 0)

	passed, _, _ := mgr.verifyGateState(context.Background(), sess, workSince(history))
	if !passed {
		t.Fatal("declared verify command no longer satisfies the gate after screening")
	}
}
