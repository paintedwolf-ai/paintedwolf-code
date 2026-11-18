package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writeProjectFile(t *testing.T, projectDir, basename, body string) string {
	t.Helper()
	path := settingsoverlay.ProjectOverlayPath(projectDir, basename)
	testutil.FailErr(t, "create overlay", os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write "+basename, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// projectStore is what each of the three project settings stores shares.
type projectStore interface {
	ProjectRejected(projectDir string) []ProjectRejection
}

func requireRefused(t *testing.T, store projectStore, projectDir, basename, path, before string, put func() error) {
	t.Helper()
	rejected := store.ProjectRejected(projectDir)
	if len(rejected) != 1 || !strings.Contains(rejected[0].Detail, settingsoverlay.Rel(basename)) {
		t.Fatalf("%s rejections = %+v, want one naming the file", basename, rejected)
	}
	if err := put(); !errors.Is(err, ErrProjectSettingsNeedRepair) {
		t.Fatalf("%s save over an unreadable file: err = %v", basename, err)
	}
	if got, _ := os.ReadFile(path); string(got) != before {
		t.Fatalf("%s was overwritten:\n%s", basename, got)
	}
}

func TestProjectLimitsReportAnUnreadableFileAndKeepTheDevicesLimits(t *testing.T) {
	store, err := NewLimitsStoreAt(filepath.Join(t.TempDir(), "limits.yaml"))
	testutil.FailErr(t, "NewLimitsStoreAt", err)
	device := store.Get(llm.SettingsScopeGlobal, "")
	for name, body := range map[string]string{
		"malformed":      "max_iterations: [\n",
		"misspelled key": "max_iteration: 7\n",
	} {
		t.Run(name, func(t *testing.T) {
			projectDir := t.TempDir()
			path := writeProjectFile(t, projectDir, settingsoverlay.BasenameLimits, body)
			if got := store.Get(llm.SettingsScopeProject, projectDir); got != device {
				t.Fatalf("limits = %+v, want the device's %+v", got, device)
			}
			requireRefused(t, store, projectDir, settingsoverlay.BasenameLimits, path, body, func() error {
				return store.PutProject(projectDir, SessionLimits{MaxIterations: 9})
			})
		})
	}
	projectDir := t.TempDir()
	writeProjectFile(t, projectDir, settingsoverlay.BasenameLimits, "max_iterations: 7\n")
	if got := store.Get(llm.SettingsScopeProject, projectDir); got.MaxIterations != 7 || len(store.ProjectRejected(projectDir)) != 0 {
		t.Fatalf("a valid file was not applied: %+v", got)
	}
	empty := t.TempDir()
	writeProjectFile(t, empty, settingsoverlay.BasenameLimits, "")
	if len(store.ProjectRejected(empty)) != 0 || len(store.ProjectRejected(t.TempDir())) != 0 {
		t.Fatal("an empty or missing file was refused")
	}
}

func TestProjectVerifyReportsAnUnreadableFileAndDeclaresNoCommand(t *testing.T) {
	dir := t.TempDir()
	store, err := NewVerifyStoreAt(filepath.Join(dir, "verify.yaml"), filepath.Join(dir, "detect.json"))
	testutil.FailErr(t, "NewVerifyStoreAt", err)
	projectDir := t.TempDir()
	body := "tests: go test ./...\n"
	path := writeProjectFile(t, projectDir, settingsoverlay.BasenameVerify, body)
	if got := store.Get(llm.SettingsScopeProject, projectDir).Test; got != "" {
		t.Fatalf("test command = %q from a refused file", got)
	}
	requireRefused(t, store, projectDir, settingsoverlay.BasenameVerify, path, body, func() error {
		return store.PutProject(projectDir, VerifyConfig{Test: "make test"})
	})
	if dto := VerifyToDTO("project", projectDir, VerifyConfig{}, VerifyDetectCandidate{Command: "go test ./..."}, true, store.ProjectRejected(projectDir)); dto.SuggestionState != "" || dto.DetectedCommand != "" || len(dto.Rejected) != 1 {
		t.Fatalf("a suggestion was offered over a file that must be fixed first: %+v", dto)
	}
}

func TestProjectReviewFailsClosedWhileItsFileIsUnreadable(t *testing.T) {
	store, err := NewReviewStoreAt(filepath.Join(t.TempDir(), "review.yaml"))
	testutil.FailErr(t, "NewReviewStoreAt", err)
	projectDir := t.TempDir()
	body := "review_paths:\n  - path: secrets/**\n    reson: typo\n"
	path := writeProjectFile(t, projectDir, settingsoverlay.BasenameReview, body)
	cfg := store.Get(llm.SettingsScopeProject, projectDir)
	if !cfg.MatchesReviewPath("write", "src/any/file.go") {
		t.Fatalf("an edit did not ask for review while review.yaml was unreadable: %+v", cfg.ReviewPaths)
	}
	requireRefused(t, store, projectDir, settingsoverlay.BasenameReview, path, body, func() error {
		return store.PutProject(projectDir, ReviewConfig{})
	})
	if got := ReviewToDTO("project", cfg, nil, store.ProjectRejected(projectDir)); len(got.Rejected) != 1 || got.Rejected[0].Code != ProjectSettingsUnreadable {
		t.Fatalf("review response rejected = %+v", got.Rejected)
	}
	healthy := t.TempDir()
	writeProjectFile(t, healthy, settingsoverlay.BasenameReview, "review_paths:\n  - path: secrets/**\n")
	if got := store.Get(llm.SettingsScopeProject, healthy); got.MatchesReviewPath("write", "src/any/file.go") || !got.MatchesReviewPath("write", "secrets/key.pem") {
		t.Fatalf("a readable review.yaml did not apply as written: %+v", got.ReviewPaths)
	}
}
