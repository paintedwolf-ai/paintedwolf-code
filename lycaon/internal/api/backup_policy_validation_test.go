//go:build integration

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/backup"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRestoreRejectsMalformedRetentionPolicyWithoutChangingLiveStore(t *testing.T) {
	sourceRoot := t.TempDir()
	sourcePath := filepath.Join(sourceRoot, "store.db")
	source := testdbfixture.OpenPath(t, sourcePath)
	valid, err := json.Marshal(historyretention.DefaultPolicy())
	testutil.FailErr(t, "encode valid retention policy", err)
	// Each malformed policy is archived once; both server modes restore the same bytes.
	archives := make(map[string][]byte)
	for name, body := range map[string]string{
		"empty": "", "syntax": "{", "array": "[]", "null": "null", "missing fields": "{}",
		"unknown field":   strings.TrimSuffix(string(valid), "}") + `,"private_archive_field":true}`,
		"trailing object": string(valid) + " {}",
		"unknown mode":    strings.Replace(string(valid), `"forever"`, `"private_archive_field"`, 1),
	} {
		testutil.FailErr(t, "write archived policy", os.WriteFile(filepath.Join(sourceRoot, historyretention.PolicyFilename), []byte(body), 0o600))
		archivePath := filepath.Join(t.TempDir(), "backup.zip")
		_, err := backup.Create(t.Context(), backup.CreateOpts{ConfigDir: sourceRoot, DBPath: sourcePath, SQLDB: source, SchemaUserVersion: db.SchemaVersion}, archivePath)
		testutil.FailErr(t, "create checksummed archive", err)
		archives[name], err = os.ReadFile(archivePath)
		testutil.FailErr(t, "read policy archive", err)
	}
	for _, recovery := range []bool{false, true} {
		name := "normal"
		if recovery {
			name = "recovery"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "store.db")
			var srv *Server
			if recovery {
				testutil.FailErr(t, "write refused live store", os.WriteFile(path, []byte("refused fixture store"), 0o600))
				srv = NewRecoveryServer(t.Context(), RecoveryServerOpts{DataDir: root, DBPath: path, APIToken: TestAPIToken})
			} else {
				database := testdbfixture.OpenPath(t, path)
				srv = newTestServer(t, withSessionStore(store.NewSQL(database)), func(d *Dependencies) {
					d.Storage.DataDir, d.Storage.StorePath = root, path
				})
			}
			testutil.FailErr(t, "seed live retention policy", os.WriteFile(filepath.Join(root, historyretention.PolicyFilename), valid, 0o600))
			testutil.FailErr(t, "seed live credential", os.WriteFile(filepath.Join(root, "credential-vault.age"), []byte("keep fixture credential"), 0o600))
			protected := make(map[string][]byte)
			for _, rel := range []string{"store.db", "store.db-wal", historyretention.PolicyFilename, "credential-vault.age"} {
				body, readErr := os.ReadFile(filepath.Join(root, rel))
				if rel == "store.db-wal" && os.IsNotExist(readErr) {
					continue
				}
				testutil.FailErr(t, "capture protected file", readErr)
				protected[rel] = body
			}
			for name, archive := range archives {
				t.Run(name, func(t *testing.T) {
					request := newAuthedRequest(http.MethodPost, "/v1/backup/restore", bytes.NewReader(archive))
					request.Header.Set("Content-Type", "application/zip")
					response := httptest.NewRecorder()
					srv.ServeHTTP(response, request)
					assertErrorResponse(t, response, http.StatusBadRequest, "backup_invalid")
					if strings.Contains(response.Body.String(), "private_archive_field") {
						t.Fatal("response exposed archived policy contents")
					}
					assertPolicyRestorePreservesFiles(t, srv, root, protected)
				})
			}
		})
	}
}

func assertPolicyRestorePreservesFiles(t *testing.T, srv *Server, root string, protected map[string][]byte) {
	t.Helper()
	if srv.restorePending.Load() {
		t.Fatal("rejected policy fenced the live server")
	}
	for rel, want := range protected {
		got, err := os.ReadFile(filepath.Join(root, rel))
		testutil.FailErr(t, "read protected restore target", err)
		if !bytes.Equal(got, want) {
			t.Errorf("rejected policy changed %s", rel)
		}
	}
	if _, err := os.Stat(backup.PendingMarkerPath(root)); !os.IsNotExist(err) {
		t.Fatalf("rejected policy left pending restore: %v", err)
	}
	for _, prefix := range []string{localdata.RestoreStagingDirPrefix, localdata.RestorePreImageDirPrefix, ".restore-upload"} {
		paths, err := filepath.Glob(filepath.Join(root, prefix+"-*"))
		testutil.FailErr(t, "inspect rejected policy staging", err)
		if len(paths) != 0 {
			t.Errorf("rejected policy left transaction files: %v", paths)
		}
	}
}
