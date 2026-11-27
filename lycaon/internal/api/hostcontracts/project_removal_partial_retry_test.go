package hostcontracts

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	"github.com/lycaon/lycaon/internal/testutil/extstatetest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestProjectRemovalEndpointRetriesInterruptedPartialDeletion(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	database := testdbfixture.Open(t, "removals-partial.db")
	reg := project.NewSQLRegistry(database)
	rootDir := t.TempDir()
	sentinel := filepath.Join(rootDir, "keep.txt")
	testutil.FailErr(t, "write root sentinel", os.WriteFile(sentinel, []byte("untouched\n"), 0o600))

	p, err := reg.Create(t.Context(), project.CreateParams{
		Name:  "Interrupted",
		Roots: []project.AttachRootParams{{Path: rootDir}},
	})
	testutil.FailErr(t, "create project", err)

	dataDir := t.TempDir()
	hostDataDir, err := project.EnsureHostDataDir(dataDir, p.ID)
	testutil.FailErr(t, "ensure host data dir", err)

	deps := hostapi.Dependencies{
		Core:    hostapi.CoreDependencies{Store: sessionstore.NewSQL(database), Projects: reg},
		Storage: hostapi.StorageDependencies{DataDir: dataDir},
	}
	contractfixture.WithExtensionOwner(t)(&deps)
	server := hostapi.NewServer(contractfixture.RequiredTestDeps(t, deps), nil, hostapi.TestAPIToken)
	owner := server.Admin.Extensions.Mutations.Owner
	pack := t.TempDir()
	extpackstest.WriteMinimalPack(t, pack, "acme/partial-cleanup", 1)
	extstatetest.Apply(t, owner, extstatetest.DeviceScope(), extensionstate.InstallOp{Source: "path:" + pack})
	extstatetest.Apply(t, owner, extstatetest.DeviceScope(), extensionstate.SetInstalledFromOp{PackID: "acme/partial-cleanup", ProjectID: p.ID})

	// Simulate an interrupted deletion where child rows (including project_roots
	// and outside namespace) were removed while the projects row survived.
	testutil.FailErr(t, "delete child project rows", db.DeleteProjectRows(t.Context(), database, p.ID))

	// Normal Get reports ErrNotFound so search/reads/writes cannot touch it.
	if _, err := reg.Get(t.Context(), p.ID); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("Registry.Get during partial delete = %v, want ErrNotFound", err)
	}

	// Real public removal endpoints still assess and finish the interrupted removal.
	path := "/v1/projects/" + p.ID
	assessRec := httptest.NewRecorder()
	server.ServeHTTP(assessRec, contractfixture.NewAuthedRequest(http.MethodGet, path+"/removal-assessment", nil))
	if assessRec.Code != http.StatusOK {
		t.Fatalf("assessment on partial delete = %d %s", assessRec.Code, assessRec.Body.String())
	}
	var assessment wire.ProjectRemovalAssessment
	testutil.FailErr(t, "decode assessment", json.Unmarshal(assessRec.Body.Bytes(), &assessment))

	req := wire.ProjectRemovalRequest{
		OperationID:      uuid.NewString(),
		AssessmentToken:  assessment.AssessmentToken,
		RemoveExtensions: []string{"acme/partial-cleanup"},
	}
	body, err := json.Marshal(req)
	testutil.FailErr(t, "marshal removal request", err)
	remRec := httptest.NewRecorder()
	server.ServeHTTP(remRec, contractfixture.NewAuthedRequest(http.MethodPost, path+"/removals", bytes.NewReader(body)))
	if remRec.Code != http.StatusAccepted {
		t.Fatalf("removal retry = %d %s", remRec.Code, remRec.Body.String())
	}
	contractfixture.DrainBackground(t, server)

	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, contractfixture.NewAuthedRequest(http.MethodGet, path+"/removals/"+req.OperationID, nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("removal status = %d %s", statusRec.Code, statusRec.Body.String())
	}
	var result wire.ProjectRemovalResult
	testutil.FailErr(t, "decode removal result", json.Unmarshal(statusRec.Body.Bytes(), &result))
	if result.ProjectState != "deleted" || result.CleanupState != "removed" {
		t.Fatalf("removal retry result = %+v, want deleted/removed", result)
	}

	// Cleanup ran (host data dir removed) without touching the user's deleted root bytes.
	if _, err := os.Stat(hostDataDir); !os.IsNotExist(err) {
		t.Fatalf("host data dir still exists after retry: %v", err)
	}
	gotSentinel, err := os.ReadFile(sentinel)
	testutil.FailErr(t, "read root sentinel", err)
	if string(gotSentinel) != "untouched\n" {
		t.Fatalf("root sentinel mutated: %q", gotSentinel)
	}
	if _, err := project.GetForRemoval(t.Context(), reg, p.ID); !errors.Is(err, project.ErrNotFound) {
		t.Fatalf("GetForRemoval after retry = %v, want ErrNotFound", err)
	}
}
