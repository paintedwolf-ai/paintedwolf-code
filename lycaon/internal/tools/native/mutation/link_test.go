package mutation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcerewind"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSymbolicLinkCreatesReadsAndReplacesItsEntry(t *testing.T) {
	h := newHarness(t, map[string]string{"target.txt": "linked bytes\n"})
	created := h.run("", "ln", "-s", "target.txt", "link.txt")
	if created.err != nil || created.result.Status != 0 {
		t.Fatalf("create link: %+v", created)
	}
	if got := h.read("link.txt"); got != "linked bytes\n" {
		t.Fatalf("linked read = %q", got)
	}
	if text, err := os.Readlink(h.path("link.txt")); err != nil || text != "target.txt" {
		t.Fatalf("link text = %q, %v", text, err)
	}
	head, err := h.tc.Source.History.Files.ResolveHead(t.Context(), "p1", sourcebranch.Trunk, "r1", "link.txt")
	if err != nil || head.State != "link" {
		t.Fatalf("source head = %+v, %v", head, err)
	}
	retained, err := h.tc.Source.History.Files.ReadRestorableVersion(t.Context(), "p1", head.VersionID)
	if err != nil || retained.State != "link" || string(retained.Content) != "target.txt" {
		t.Fatalf("retained link = %+v, %v", retained, err)
	}
	seen, err := h.walk.QueryWalk(t.Context(), "p1", sourceledger.Baseline{}, 20, 0, sourceledger.CommitLens{})
	testutil.FailErr(t, "h.walk.QueryWalk failed", err)
	found := false
	for _, file := range seen.Files {
		if file.Path == "link.txt" && file.Tip.State == api.SourceTipStateContent {
			found = true
		}
	}
	if !found {
		t.Fatalf("link missing from Files: %+v", seen.Files)
	}
	replaced := h.run("", "ln", "-sfnv", "missing.txt", "link.txt")
	if replaced.err != nil || replaced.result.Status != 0 || !strings.Contains(replaced.stdout, "link.txt") {
		t.Fatalf("replace link: %+v", replaced)
	}
	if text, err := os.Readlink(h.path("link.txt")); err != nil || text != "missing.txt" {
		t.Fatalf("replacement text = %q, %v", text, err)
	}
	if got := h.read("target.txt"); got != "linked bytes\n" {
		t.Fatalf("original target changed = %q", got)
	}
	updated, err := h.tc.Source.History.Files.ResolveHead(t.Context(), "p1", sourcebranch.Trunk, "r1", "link.txt")
	if err != nil || updated.State != "link" || updated.VersionID == head.VersionID {
		t.Fatalf("updated source head = %+v, %v", updated, err)
	}
	updatedVersion, err := h.tc.Source.History.Files.ReadRestorableVersion(t.Context(), "p1", updated.VersionID)
	testutil.FailErr(t, "h.tc.Source.History.Files.ReadRestorableVersion failed", err)
	rewind := &sourcerewind.Operation{AttemptID: uuid.NewString(), ProjectID: "p1", WorkspaceID: h.project.WorkspaceID(), Phase: "prepared",
		Files: []sourcerewind.File{{RewindFile: sourceledger.RewindFile{FileID: updatedVersion.FileID, BranchID: sourcebranch.Trunk,
			Expected: updatedVersion, Target: retained}, RootPath: h.root, AllowedRoots: []string{h.root}, Mode: 0o644}}}
	if err := (&sourcerewind.Service{Mutations: h.service}).Apply(t.Context(), h.project, rewind, func() error { return nil }); err != nil {
		t.Fatalf("rewind link replacement: %v", err)
	}
	if text, err := os.Readlink(h.path("link.txt")); err != nil || text != "target.txt" {
		t.Fatalf("restored link text = %q, %v", text, err)
	}
}

func TestSymbolicLinkGNUAndBSDOptionSpellings(t *testing.T) {
	h := newHarness(t, map[string]string{"target.txt": "content"})
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"-srv", "target.txt", "short.txt"}, "short.txt"},
		{[]string{"--symbolic", "--relative", "--verbose", "target.txt", "long.txt"}, "long.txt"},
	} {
		result := h.run("", append([]string{"ln"}, tc.args...)...)
		if result.err != nil || result.result.Status != 0 {
			t.Fatalf("ln %v: %+v", tc.args, result)
		}
		if text, err := os.Readlink(h.path(tc.path)); err != nil || text != "target.txt" {
			t.Fatalf("link %s text = %q, %v", tc.path, text, err)
		}
	}
}

func TestSymbolicLinkForceReplacesRegularEntryWithSeparateHistory(t *testing.T) {
	h := newHarness(t, map[string]string{"target.txt": "target", "link.txt": "old"})
	result := h.run("", "ln", "-sf", "target.txt", "link.txt")
	if result.err != nil || result.result.Status != 0 {
		t.Fatalf("replace regular file: %+v", result)
	}
	if text, err := os.Readlink(h.path("link.txt")); err != nil || text != "target.txt" {
		t.Fatalf("replacement link = %q, %v", text, err)
	}
	head, err := h.tc.Source.History.Files.ResolveHead(t.Context(), "p1", sourcebranch.Trunk, "r1", "link.txt")
	if err != nil || head.State != "link" {
		t.Fatalf("replacement head = %+v, %v", head, err)
	}
}

func TestSymbolicLinkNoDereferenceSpellingsReplaceDirectoryLink(t *testing.T) {
	for _, spelling := range []string{"-n", "-h", "--no-dereference"} {
		t.Run(spelling, func(t *testing.T) {
			h := newHarness(t, map[string]string{"target.txt": "target", "dir/": ""})
			if err := os.Symlink("dir", h.path("folder-link")); err != nil {
				testutil.FailErr(t, "os.Symlink failed", err)
			}
			result := h.run("", "ln", "-sf", spelling, "target.txt", "folder-link")
			if result.err != nil || result.result.Status != 0 {
				t.Fatalf("replace directory link: %+v", result)
			}
			if text, err := os.Readlink(h.path("folder-link")); err != nil || text != "target.txt" {
				t.Fatalf("link text = %q, %v", text, err)
			}
			if _, err := os.Lstat(h.path("dir/target.txt")); !os.IsNotExist(err) {
				t.Fatalf("created inside former directory: %v", err)
			}
		})
	}
}

func TestSymbolicLinkRefusesEscapeAndHardLinks(t *testing.T) {
	h := newHarness(t, map[string]string{"target.txt": "kept"})
	outside := filepath.Join(t.TempDir(), "outside")
	for _, target := range []string{outside, filepath.Join("..", "outside")} {
		result := h.run("", "ln", "-s", target, "link.txt")
		if reject := toolrejection.AsToolReject(result.err); reject == nil || reject.Code != "SURVEY_PATH_ESCAPE" {
			t.Fatalf("escape %q: %+v", target, result)
		}
		if !h.absent("link.txt") {
			t.Fatal("escaped link appeared")
		}
	}
	hard := h.run("", "ln", "target.txt", "link.txt")
	if reject := toolrejection.AsToolReject(hard.err); reject == nil || reject.Code != "COMMAND_NATIVE_FORM_UNSUPPORTED" {
		t.Fatalf("hard link: %+v", hard)
	}
	if !h.absent("link.txt") {
		t.Fatal("hard link appeared")
	}
	outsideDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(outsideDir, "child"), 0o755); err != nil {
		testutil.FailErr(t, "os.Mkdir failed", err)
	}
	if err := os.Symlink(filepath.Join(outsideDir, "child"), h.path("alias")); err != nil {
		testutil.FailErr(t, "os.Symlink failed", err)
	}
	escape := h.run("", "ln", "-s", "alias/../outside", "link.txt")
	if reject := toolrejection.AsToolReject(escape.err); reject == nil || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("symlink-parent escape: %+v", escape)
	}
}
