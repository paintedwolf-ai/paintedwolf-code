package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func attachCredentialRoot(t *testing.T, files *credentialFiles) string {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "attach fixture root", files.queries.CreateProjectRoot(t.Context(), db.CreateProjectRootParams{ID: "r1", ProjectID: "proj-1", Path: root, Label: "root", IsPrimary: 1, AddedAt: "2026-01-01T00:00:00Z", Kind: "attached"}))
	return root
}

func TestCredentialIndexPrecedesAnyDelivery(t *testing.T) {
	files := credentialFilesFixture(t, "managed-value-4411")
	root := attachCredentialRoot(t, files)
	testutil.FailErr(t, "record model authorship", files.Authored(t.Context(), tools.AuthoredCredentialValues{ProjectID: "proj-1", RootID: "r1", Path: ".env", RootSessionID: "chat", Values: []string{"model-authored-5533"}}))
	body := "DB_PASSWORD=person-value-6622\nOTHER=managed-value-4411\nMODEL=model-authored-5533\n"
	testutil.FailErr(t, "write credential fixture", os.WriteFile(filepath.Join(root, ".env"), []byte(body), 0600))
	testutil.FailErr(t, "write template", os.WriteFile(filepath.Join(root, ".env.example"), []byte("DB_PASSWORD=template-value-9944"), 0600))
	exposure := 0
	files.exposure = func(context.Context, string, string) error { exposure++; return nil }
	testutil.FailErr(t, "admit project", files.EnsureProject(t.Context(), "proj-1"))
	got := files.harvest.FileValues("proj-1")
	if len(got) != 1 || got[0].Secret() != "person-value-6622" || exposure != 0 {
		t.Fatalf("evidence=%+v exposure=%d", got, exposure)
	}
	if len(files.harvest.FileValues("another-project")) != 0 {
		t.Fatal("file evidence crossed project scope")
	}
	if len(files.harvest.AllValues()) != 1 || len(files.harvest.ValuesForProject("proj-1")) != 1 {
		t.Fatal("unattributed or project screening omitted file evidence")
	}
}

func TestCredentialIndexRetainsRemovedBindings(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	path := filepath.Join(root, ".env")
	testutil.FailErr(t, "write first binding", os.WriteFile(path, []byte("DB_PASSWORD=first-value-4411"), 0600))
	testutil.FailErr(t, "admit root", files.EnsureProject(t.Context(), "proj-1"))
	testutil.FailErr(t, "rotate file binding", os.WriteFile(path, []byte("DB_PASSWORD=second-value-5533"), 0600))
	testutil.FailErr(t, "observe changed credential", files.index.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{".env"}}))
	values := files.harvest.FileValues("proj-1")
	retired, active := false, false
	for _, value := range values {
		retired = retired || value.Secret() == "first-value-4411" && value.Retired
		active = active || value.Secret() == "second-value-5533" && !value.Retired
	}
	if !retired || !active || len(values) != 2 {
		t.Fatalf("rotation evidence = %+v", values)
	}
	testutil.FailErr(t, "delete credential fixture", os.Remove(path))
	testutil.FailErr(t, "observe deletion", files.index.changed(t.Context(), repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged, Paths: []string{".env"}}))
	for _, value := range files.harvest.FileValues("proj-1") {
		if !value.Retired {
			t.Fatalf("deleted file retained active binding: %+v", value)
		}
	}
}

func TestCredentialIndexDoesNotFollowLinks(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	outside := filepath.Join(t.TempDir(), ".env")
	testutil.FailErr(t, "write outside fixture", os.WriteFile(outside, []byte("DB_PASSWORD=outside-value-7733"), 0600))
	testutil.FailErr(t, "link outside fixture", os.Symlink(outside, filepath.Join(root, ".env")))
	testutil.FailErr(t, "index root", files.EnsureProject(t.Context(), "proj-1"))
	if len(files.harvest.FileValues("proj-1")) != 0 {
		t.Fatal("symlink supplied credential evidence outside the root")
	}
}

func TestCredentialIndexFailureCannotAdmitPartialCapture(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	file, err := os.Create(filepath.Join(root, ".env"))
	testutil.FailErr(t, "create oversize fixture", err)
	testutil.FailErr(t, "size fixture", file.Truncate(credentialFileBytes+1))
	testutil.FailErr(t, "close fixture", file.Close())
	if err := files.EnsureProject(t.Context(), "proj-1"); err == nil {
		t.Fatal("oversize credential capture was admitted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := files.EnsureProject(ctx, "proj-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capture = %v", err)
	}
	testutil.FailErr(t, "repair file", os.WriteFile(filepath.Join(root, ".env"), []byte("DB_PASSWORD=recovered-value-4411"), 0600))
	testutil.FailErr(t, "retry capture", files.EnsureProject(t.Context(), "proj-1"))
	if values := files.harvest.FileValues("proj-1"); len(values) != 1 || !strings.Contains(values[0].Secret(), "recovered") {
		t.Fatalf("recovery evidence = %+v", values)
	}
}
