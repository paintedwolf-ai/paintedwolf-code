package harnessfixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteResourceOwnershipAndBoundary(t *testing.T) {
	capture, project := t.TempDir(), t.TempDir()
	roots := []string{capture, project}
	resource, err := allocateWriteResource(WriteResourceRequest{Capture: capture, Project: project}, t.TempDir(), roots)
	testutil.FailErr(t, "allocate external output", err)
	t.Cleanup(func() { _ = os.RemoveAll(resource.Path) })
	if confine.PathWithinWriteRoots(resource.Path, roots) {
		t.Fatal("output can be written without a grant")
	}
	if confine.PathWithinWriteRoots(resource.Path, []string{capture}) {
		t.Fatal("output exposes the capture directory")
	}
	info, err := os.Stat(resource.Path)
	testutil.FailErr(t, "inspect output", err)
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("output permissions = %v", info.Mode())
	}
	body, err := os.ReadFile(filepath.Join(capture, "external-resources", resource.ID+".json"))
	testutil.FailErr(t, "read ownership", err)
	var recorded WriteResource
	testutil.FailErr(t, "decode ownership", json.Unmarshal(body, &recorded))
	if recorded != resource {
		t.Fatalf("ownership = %+v; resource = %+v", recorded, resource)
	}
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "resolve home", err)
	refused := t.TempDir()
	if _, err := PrepareWriteResource(WriteResourceRequest{Capture: refused, Project: home}); err == nil {
		t.Fatal("output inside an attached root was accepted")
	}
	entries, err := os.ReadDir(refused)
	testutil.FailErr(t, "inspect refused allocation", err)
	if len(entries) != 0 {
		t.Fatal("refused allocation wrote an ownership receipt")
	}
}

func TestWriteResourceRejectsRelativeOwnership(t *testing.T) {
	for _, request := range []WriteResourceRequest{{Capture: "relative", Project: t.TempDir()}, {Capture: t.TempDir(), Project: "relative"}} {
		if _, err := PrepareWriteResource(request); err == nil {
			t.Fatal("relative ownership accepted")
		}
	}
}

func TestWriteResourceReleaseValidatesOwnership(t *testing.T) {
	for _, mutation := range []string{"none", "path", "capture", "symlink", "parent_symlink"} {
		t.Run(mutation, func(t *testing.T) {
			capture, project, base := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "output")
			resource, err := allocateWriteResource(WriteResourceRequest{Capture: capture, Project: project}, base, nil)
			testutil.FailErr(t, "allocate output", err)
			target := t.TempDir()
			testutil.FailErr(t, "seed unrelated content", os.WriteFile(filepath.Join(target, "keep"), []byte("keep"), 0o600))
			owner := resource
			switch mutation {
			case "path":
				owner.Path = target
			case "capture":
				owner.Capture = target
			case "symlink":
				testutil.FailErr(t, "remove owned directory", os.Remove(resource.Path))
				testutil.FailErr(t, "replace output with link", os.Symlink(target, resource.Path))
			case "parent_symlink":
				testutil.FailErr(t, "remove owned parent", os.RemoveAll(base))
				testutil.FailErr(t, "replace parent with link", os.Symlink(target, base))
			}
			body, err := json.Marshal(owner)
			testutil.FailErr(t, "encode owner", err)
			testutil.FailErr(t, "record owner", os.WriteFile(filepath.Join(capture, "external-resources", resource.ID+".json"), body, 0o600))
			err = releaseWriteResources(capture, base)
			if mutation == "none" {
				testutil.FailErr(t, "release output", err)
				testutil.FailErr(t, "release idempotently", releaseWriteResources(capture, base))
				if _, err := os.Stat(resource.Path); !os.IsNotExist(err) {
					t.Fatal("output remains")
				}
			} else if err == nil {
				t.Fatal("invalid ownership accepted")
			}
			if body, err := os.ReadFile(filepath.Join(target, "keep")); err != nil || string(body) != "keep" {
				t.Fatal("unrelated content changed")
			}
		})
	}
}
