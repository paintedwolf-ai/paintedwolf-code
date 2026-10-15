package extpacks_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveStockCatalogUsesBundledPacksOnly(t *testing.T) {
	t.Parallel()
	eff, err := extpacks.ResolveStockCatalog(t.Context(), nil)
	testutil.FailErr(t, "resolve stock catalog", err)
	platformLoaded := false
	for _, pack := range eff.Packs {
		if pack.Kind != extpacks.PackKindStock && pack.Contributing {
			t.Fatalf("non-stock pack %q contributes to the stock catalog", pack.ID)
		}
		if pack.ID == "painted-wolf/platform" && pack.Contributing {
			platformLoaded = true
		}
	}
	if !platformLoaded {
		t.Fatal("painted-wolf/platform must contribute to the stock catalog")
	}
	if len(eff.Desired.Packs) != 0 || len(eff.Desired.Disabled) != 0 {
		t.Fatalf("stock desired state = %+v, want empty", eff.Desired)
	}
}

func TestSecurityPackVersionResolvesWithPlatformDependency(t *testing.T) {
	eff, err := extpacks.ResolveStockCatalog(t.Context(), nil)
	testutil.FailErr(t, "resolve catalog", err)
	for _, pack := range eff.Packs {
		if pack.ID == "painted-wolf/security-survey" {
			if pack.Version != "2.0.0" || !pack.Contributing {
				t.Fatalf("security pack = %+v", pack)
			}
			return
		}
	}
	t.Fatal("security pack is missing")
}

func TestStockUpdatesUseManifestConstraintsAndIsolateDependents(t *testing.T) {
	for _, tc := range []struct {
		name, dependency, version, constraint string
		want                                  bool
	}{
		{"patch", extpacks.PlatformPackID, "1.0.1", "^1.0.0", true},
		{"minor", extpacks.PlatformPackID, "1.1.0", "^1.0.0", true},
		{"major", extpacks.PlatformPackID, "2.0.0", "^1.0.0", false},
		{"exact", extpacks.PlatformPackID, "1.0.1", "1.0.0", false},
		{"missing", "painted-wolf/missing", "1.0.0", "^1.0.0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stock, err := extpacks.DiscoverStockContent()
			testutil.FailErr(t, "discover stock", err)
			for i := range stock {
				if stock[i].Manifest.ID == extpacks.PlatformPackID {
					stock[i].Manifest.Version = tc.version
				}
			}
			dependent := extpacks.PackContent{
				Pack: extpacks.Pack{ID: "acme/dependent"}, Kind: extpacks.PackKindGit,
				Manifest: extpacks.Manifest{ID: "acme/dependent", Version: "1.0.0", Compatibility: extpacks.ManifestCompatibility{ExtensionAPI: "^1.0.0"}, Dependencies: map[string]extpacks.DependencyRequest{tc.dependency: {Version: tc.constraint}}},
				Locked:   &extpacks.LockedPackage{Dependencies: map[string]string{tc.dependency: "1.0.0"}},
			}
			unrelated := extpacks.PackContent{Pack: extpacks.Pack{ID: "acme/unrelated"}, Kind: extpacks.PackKindGit, Manifest: extpacks.Manifest{ID: "acme/unrelated", Version: "1.0.0", Compatibility: extpacks.ManifestCompatibility{ExtensionAPI: "^1.0.0"}}}
			desired := extpacks.EmptyDesired()
			desired.Packs = []extpacks.DesiredPack{{ID: dependent.Pack.ID}, {ID: unrelated.Pack.ID}}
			eff := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: append(stock, dependent, unrelated), Desired: desired})
			if eff.PackContributed(dependent.Pack.ID) != tc.want {
				t.Fatalf("dependent contribution: %+v", eff.Packs)
			}
			if !eff.PackContributed(unrelated.Pack.ID) {
				t.Fatal("stock incompatibility disabled an unrelated extension")
			}
			var found bool
			for _, pack := range eff.Packs {
				if pack.ID == dependent.Pack.ID {
					wantVersion := tc.version
					if tc.dependency != extpacks.PlatformPackID {
						wantVersion = ""
					}
					if pack.Dependencies[tc.dependency] != wantVersion {
						t.Fatalf("dependency projection: %+v", pack)
					}
					if !tc.want && pack.BlockedReason != extpacks.BlockedRequires {
						t.Fatalf("blocked reason: %+v", pack)
					}
				}
			}
			for _, d := range eff.Diagnostics {
				if d.PackID == dependent.Pack.ID && d.Code == extpacks.DiagRequiresUnmet {
					found = true
				}
			}
			if found == tc.want {
				t.Fatalf("dependency diagnostic=%v, contributing=%v", found, tc.want)
			}
		})
	}
}
