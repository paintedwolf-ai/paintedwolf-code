package detectionpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func writeProjectOverlay(t *testing.T, body string) string {
	t.Helper()
	projectDir := t.TempDir()
	path := ProjectPacksPath(projectDir)
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(filepath.Dir(path), 0o700))
	testutil.FailErr(t, "write overlay", os.WriteFile(path, []byte(body), 0o600))
	return projectDir
}

// A clone arrives with its overlay already in the tree and the trust surface
// that governs it defaults to on, so an `enabled: false` row would let content
// nobody has read yet decide which destructive-effect rules never fire. The
// project tier turns packs on and only on.
func TestProjectOverlayEnablesButCannotDisable(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	testutil.FailErr(t, "WriteDisabledIDs", WriteDisabledIDs(cfg, []string{"gcloud-cli"}))
	projectDir := writeProjectOverlay(t, `
packs:
  - id: aws-cli
    enabled: false
  - id: gcloud-cli
    enabled: true
`)

	cat, err := LoadCatalog(deviceInput(t, cfg, projectDir))
	testutil.FailErr(t, "LoadCatalog", err)
	if len(cat.Warnings) != 0 {
		t.Fatalf("warnings: %v", cat.Warnings)
	}
	if p, ok := cat.PackByID("aws-cli"); !ok || !p.Enabled {
		t.Fatalf("aws-cli enabled=%v ok=%v; a project row turned a pack off", p.Enabled, ok)
	}
	// The refusal is visible: Settings has to be able to say the line did nothing.
	found := false
	for _, row := range cat.Rejected {
		if row.ID == "aws-cli" && row.Code == RejectInvalidEntry {
			found = true
		}
	}
	if !found {
		t.Fatalf("a refused disable must surface as a rejected row, got %+v", cat.Rejected)
	}
	// The project tier lands after the device state, so it can turn a pack the
	// device disabled back on for work in this tree.
	if p, ok := cat.PackByID("gcloud-cli"); !ok || !p.Enabled {
		t.Fatalf("gcloud-cli enabled=%v ok=%v; project turned it back on", p.Enabled, ok)
	}

	// The same catalogue with no project dir is untouched by the overlay.
	device, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog device", err)
	if p, _ := device.PackByID("gcloud-cli"); p.Enabled {
		t.Fatal("gcloud-cli enabled without a project dir; the tier leaked")
	}
}

func TestProjectOverlayRowFieldsAreClosed(t *testing.T) {
	t.Parallel()
	projectDir := writeProjectOverlay(t, `
packs:
  - id: aws-cli
    enabled: true
    label: Renamed
  - id: not-a-real-pack
    enabled: true
  - id: gcloud-cli
  - enabled: true
  - id: azure-cli
    enabled: true
  - id: azure-cli
    enabled: true
`)
	// azure-cli is off on the device, so whether the first row applied is
	// observable rather than indistinguishable from the default.
	cfg := t.TempDir()
	testutil.FailErr(t, "WriteDisabledIDs", WriteDisabledIDs(cfg, []string{"azure-cli"}))
	cat, err := LoadCatalog(deviceInput(t, cfg, projectDir))
	testutil.FailErr(t, "LoadCatalog", err)

	// Codes, not prose: Settings branches on the code to explain the row, so the
	// code is the part that is contract.
	byID := map[string]string{}
	for _, row := range cat.Rejected {
		byID[row.ID] = row.Code
	}
	for id, want := range map[string]string{
		"aws-cli":         RejectProjectFieldsForbidden,
		"not-a-real-pack": RejectProjectUnknownID,
		"azure-cli":       RejectDuplicateID,
		"gcloud-cli":      RejectInvalidEntry,
		"":                RejectInvalidEntry, // the row that named no pack
	} {
		if got := byID[id]; got != want {
			t.Errorf("rejected[%q] code = %q, want %q (all: %+v)", id, got, want, cat.Rejected)
		}
	}
	// Refusals are the person's business, not a host-install defect.
	if len(cat.Warnings) != 0 {
		t.Errorf("overlay refusals leaked into load warnings: %v", cat.Warnings)
	}
	for _, row := range cat.Rejected {
		if strings.TrimSpace(row.Detail) == "" {
			t.Errorf("rejected row %+v has no detail to show beside it", row)
		}
	}

	// A rejected row changes nothing, and the device answer stands.
	if p, _ := cat.PackByID("aws-cli"); !p.Enabled {
		t.Error("a row that tried to rename a pack changed the pack")
	}
	if p, _ := cat.PackByID("gcloud-cli"); !p.Enabled {
		t.Error("a row that set no enabled changed the pack")
	}
	// The first azure-cli row stands; the duplicate is dropped, not merged.
	if p, _ := cat.PackByID("azure-cli"); !p.Enabled {
		t.Error("the first azure-cli row did not apply")
	}
}

func TestProjectOverlayCannotAddAPack(t *testing.T) {
	t.Parallel()
	projectDir := writeProjectOverlay(t, `
packs:
  - id: invented-pack
    enabled: true
`)
	cat, err := LoadCatalog(deviceInput(t, t.TempDir(), projectDir))
	testutil.FailErr(t, "LoadCatalog", err)
	if _, ok := cat.PackByID("invented-pack"); ok {
		t.Fatal("project tier created a pack")
	}
	if len(cat.Rejected) != 1 || cat.Rejected[0].Code != RejectProjectUnknownID {
		t.Fatalf("rejected = %+v", cat.Rejected)
	}
	if cat.Rejected[0].ID != "invented-pack" {
		t.Fatalf("rejected row names %q; the row has to name the id the person wrote", cat.Rejected[0].ID)
	}
}

// An overlay that does not parse refuses as one row rather than N: every line
// the person wrote had no effect, which is a different fact from one bad row.
func TestProjectOverlayUnreadableRefusesWholeTier(t *testing.T) {
	t.Parallel()
	projectDir := writeProjectOverlay(t, "packs: [oh no\n")
	cat, err := LoadCatalog(deviceInput(t, t.TempDir(), projectDir))
	testutil.FailErr(t, "LoadCatalog", err)

	if len(cat.Rejected) != 1 || cat.Rejected[0].Code != RejectProjectUnreadable {
		t.Fatalf("rejected = %+v, want one %s row", cat.Rejected, RejectProjectUnreadable)
	}
	// The detail names the file at the path the person edits, not an absolute one
	// that would carry their home directory to the UI.
	detail := cat.Rejected[0].Detail
	if !strings.Contains(detail, ProjectPacksRelPath()) {
		t.Errorf("detail %q does not name %s", detail, ProjectPacksRelPath())
	}
	if strings.Contains(detail, projectDir) {
		t.Errorf("detail %q leaks the absolute project path", detail)
	}
	// A tier that could not be read silences nothing.
	if p, _ := cat.PackByID("aws-cli"); !p.Enabled {
		t.Error("an unreadable overlay disabled a pack")
	}
}

// No project directory means no project tier, so there is nothing to refuse.
func TestNoProjectDirYieldsNoRejectedRows(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(deviceInput(t, t.TempDir(), ""))
	testutil.FailErr(t, "LoadCatalog", err)
	if len(cat.Rejected) != 0 {
		t.Fatalf("rejected = %+v without a project dir", cat.Rejected)
	}
}

// Which packs are live is settled once, in the catalog the device owns, and the
// matcher is built from that. Matching takes no per-action hold-out set, so a
// project directory cannot subtract a pack from an action it is running under.
func TestMatcherHasNoPerActionHoldOut(t *testing.T) {
	t.Parallel()
	cfg := t.TempDir()
	shipped, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	ev := testEvent("command", "aws s3 rb s3://b", "/p", true, "proxy", "s")
	hit, ok := NewMatcher(shipped).Match(ev)
	if !ok || hit.PackID != "aws-cli" {
		t.Fatalf("baseline hit = %+v ok=%v", hit, ok)
	}

	// The only way a pack stops matching is the device turning it off.
	testutil.FailErr(t, "WriteDisabledIDs", WriteDisabledIDs(cfg, []string{"aws-cli"}))
	off, err := LoadCatalog(deviceInput(t, cfg, ""))
	testutil.FailErr(t, "LoadCatalog device-off", err)
	if hit, ok := NewMatcher(off).Match(ev); ok && hit.PackID == "aws-cli" {
		t.Fatal("a device-disabled pack still matched")
	}
}
