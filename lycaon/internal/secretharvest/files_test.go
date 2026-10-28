package secretharvest

import (
	"bytes"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFileEvidenceKeepsProjectAndSessionBoundaries(t *testing.T) {
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{7}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	harvest := NewRuntime(fp)
	harvest.SetProjectResolver(func(root string) string {
		if root == "chat" {
			return "project"
		}
		return "other"
	})
	harvest.BindSessionProject("chat", "project")
	growth := 0
	harvest.OnGrowth(func(root string, _ uint64) {
		if root != "chat" {
			t.Errorf("growth root=%q", root)
		}
		growth++
	})
	file := FileContent{ProjectID: "project", RootID: "root", Path: ".env", Content: []byte("TOKEN=person-value-4411")}
	harvest.ObserveFile(file)
	harvest.ObserveFile(file)
	if growth != 1 || len(harvest.FileValuesForSession("chat")) != 1 || len(harvest.FileValuesForSession("other-chat")) != 0 {
		t.Fatalf("growth=%d scope=%+v", growth, harvest.FileValues("project"))
	}
	file.Content = nil
	harvest.ObserveFile(file)
	if growth != 2 || !harvest.FileValues("project")[0].Retired {
		t.Fatal("removed bytes lost their evidence")
	}
	file.Content = []byte("TOKEN=person-value-4411")
	harvest.ObserveFile(file)
	if values := harvest.FileValues("project"); len(values) != 1 || values[0].Retired {
		t.Fatalf("restored evidence=%+v", values)
	}
	harvest.Forget("chat")
	if len(harvest.FileValues("project")) != 1 {
		t.Fatal("chat disposal removed project file evidence")
	}
}

func TestFileEvidenceSurvivesRestartAndExplicitProjectDeletion(t *testing.T) {
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{7}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	slot := credentialstore.Slot{Path: filepath.Join(t.TempDir(), "credential-vault.age"), Namespace: credentialstore.NamespaceCredentialFiles, Context: "file evidence"}
	store := credentialstore.NewEmpty(slot, nil)
	first := NewRuntime(fp)
	testutil.FailErr(t, "restore empty store", first.RestoreFiles(store))
	file := FileContent{ProjectID: "project", RootID: "root", Path: ".env", Content: []byte("TOKEN=retained-value-4411")}
	testutil.FailErr(t, "observe binding", first.ObserveFile(file))
	file.Content = nil
	testutil.FailErr(t, "observe deletion", first.ObserveFile(file))
	cipher, err := os.ReadFile(slot.Path)
	testutil.FailErr(t, "read encrypted evidence", err)
	if bytes.Contains(cipher, []byte("retained-value-4411")) {
		t.Fatal("evidence persisted plaintext")
	}
	reopened, err := credentialstore.OpenFile(slot, nil)
	testutil.FailErr(t, "reopen encrypted evidence", err)
	second := NewRuntime(fp)
	testutil.FailErr(t, "restore evidence", second.RestoreFiles(reopened))
	values := second.FileValues("project")
	if len(values) != 1 || values[0].Secret() != "retained-value-4411" || !values[0].Retired {
		t.Fatal("deleted binding did not survive restart")
	}
	file.Content = []byte("TOKEN=retained-value-4411")
	testutil.FailErr(t, "read current binding", second.ObserveFile(file))
	if values := second.FileValues("project"); len(values) != 1 || values[0].Retired {
		t.Fatal("discovery did not restore active standing")
	}
	testutil.FailErr(t, "delete project evidence", second.ForgetProject("project"))
	reopened, err = credentialstore.OpenFile(slot, nil)
	testutil.FailErr(t, "reopen deletion", err)
	third := NewRuntime(fp)
	testutil.FailErr(t, "restore deletion", third.RestoreFiles(reopened))
	if len(third.AllValues()) != 0 {
		t.Fatal("deleted project retained evidence")
	}
}

func TestFileEvidencePersistenceFailureDoesNotPublishBindings(t *testing.T) {
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{7}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	harvest := NewRuntime(fp)
	store := credentialstore.NewEmpty(credentialstore.Slot{Path: t.TempDir(), Namespace: credentialstore.NamespaceCredentialFiles, Context: "file evidence"}, nil)
	testutil.FailErr(t, "configure store", harvest.RestoreFiles(store))
	if err := harvest.ObserveFile(FileContent{ProjectID: "project", RootID: "root", Path: ".env", Content: []byte("TOKEN=person-value-4411")}); err == nil {
		t.Fatal("failed persistence admitted binding")
	}
	if len(harvest.AllValues()) != 0 {
		t.Fatal("uncommitted evidence published")
	}
}

func TestFileEvidenceRefusesUnknownFormatWithoutMutation(t *testing.T) {
	fp, err := secretmatch.NewFingerprinter(bytes.Repeat([]byte{7}, 32))
	testutil.FailErr(t, "fingerprinter", err)
	slot := credentialstore.Slot{Path: filepath.Join(t.TempDir(), "credential-vault.age"), Namespace: credentialstore.NamespaceCredentialFiles, Context: "file evidence"}
	store := credentialstore.NewEmpty(slot, nil)
	for _, raw := range []string{`{"format":2,"values":[]}`, `{"format":1,"values":[],"unknown":true}`, `{"format":1,"values":[{"name":"TOKEN","container":".env","secret":""}]}`} {
		testutil.FailErr(t, "seed unknown shape", store.Set("project", raw))
		before, err := os.ReadFile(slot.Path)
		testutil.FailErr(t, "read initial cipher", err)
		harvest := NewRuntime(fp)
		if err := harvest.RestoreFiles(store); err == nil {
			t.Fatal("unknown shape was accepted")
		}
		after, err := os.ReadFile(slot.Path)
		testutil.FailErr(t, "read preserved cipher", err)
		if !bytes.Equal(before, after) || len(harvest.AllValues()) != 0 {
			t.Fatal("unknown shape changed store or evidence")
		}
	}
}
