//go:build !windows

package persistence

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceContentPolicyUsesProjectRootAndOutsideAddresses(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "store.db")
	registry := project.NewSQLRegistry(database)
	attached := t.TempDir()
	outside := t.TempDir()
	proj, err := project.CreateWithRoot(ctx, registry, attached)
	testutil.FailErr(t, "create project", err)

	const protectedFile = "fixture-credential.json"
	previous := confine.SetCredentialStorePathsSource(func() []string {
		return []string{filepath.Join(attached, protectedFile), filepath.Join(outside, protectedFile)}
	})
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(previous) })

	outsideRoot, ok := proj.OutsideRef()
	if !ok {
		t.Fatal("missing project outside namespace")
	}
	outsideFile, ok := projectroot.OutsideRel(filepath.Join(outside, protectedFile))
	if !ok {
		t.Fatal("could not build outside credential address")
	}
	outsideOrdinary, ok := projectroot.OutsideRel(filepath.Join(outside, "notes.txt"))
	if !ok {
		t.Fatal("could not build outside ordinary address")
	}

	withheld := floorCatalogContent(db.New(database))
	for _, tc := range []struct {
		name   string
		rootID string
		path   string
		want   bool
	}{
		{"attached credential", proj.Roots[0].ID, protectedFile, true},
		{"outside credential", outsideRoot.ID, outsideFile, true},
		{"outside ordinary file", outsideRoot.ID, outsideOrdinary, false},
		{"unknown root", "unknown", outsideFile, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := withheld(t.Context(), proj.ID, tc.rootID, tc.path); got != tc.want {
				t.Fatalf("content withheld = %t, want %t", got, tc.want)
			}
		})
	}
}
