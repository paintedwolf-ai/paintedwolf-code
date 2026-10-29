//go:build linux

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/launchguard"
	"github.com/lycaon/lycaon/internal/packagegeneration"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPolicySelectsExactlyTheInstalledGenerationAndCatalogAccess(t *testing.T) {
	generation := packagegeneration.Generation{Root: "/opt/painted-wolf-code/generations/" + strings.Repeat("a", 64), ManifestSHA256: strings.Repeat("a", 64), LogicalInstallRoot: "/opt/painted-wolf-code"}
	access := packagegeneration.SystemAccess{Executables: []string{"/usr/lib/webkit/WebKitWebProcess"}, LibraryRoots: []string{"/usr/lib"}}
	policy := generationPolicy(generation, access)
	want := launchguard.GenerationPolicy{Version: 1, GenerationRoot: generation.Root, ManifestSHA256: generation.ManifestSHA256, LogicalInstallRoot: generation.LogicalInstallRoot, SystemExecutables: access.Executables, LibraryRoots: access.LibraryRoots}
	if !reflect.DeepEqual(policy, want) {
		t.Fatalf("selected launch authority = %+v, want %+v", policy, want)
	}
	if err := writePolicy(packagegeneration.Generation{}, packagegeneration.SystemAccess{}); err == nil {
		t.Fatal("real launch-policy owner accepted an invalid generation")
	}
}

func TestSelectionDistinguishesRemovalFromUnreadablePolicy(t *testing.T) {
	policy := launchguard.GenerationPolicy{GenerationRoot: "/immutable/selected"}
	for _, failure := range []error{nil, &os.PathError{Op: "open", Path: "launch.json", Err: fs.ErrNotExist}, fs.ErrPermission, errors.New("invalid launch policy identity")} {
		got, err := selectedGeneration(policy, failure)
		switch {
		case failure == nil:
			if err != nil || got != policy.GenerationRoot {
				t.Fatalf("live selection = %q, %v", got, err)
			}
		case errors.Is(failure, fs.ErrNotExist):
			if err != nil || got != "" {
				t.Fatalf("withdrawn selection = %q, %v", got, err)
			}
		default:
			if !errors.Is(err, failure) || got != "" {
				t.Fatalf("unreadable selection = %q, %v; want %v", got, err, failure)
			}
		}
	}
}

func TestWithdrawPolicyRefusesUnsafeFilesystemObjects(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "symlink", "invalid-path"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "launch.json")
			switch kind {
			case "directory":
				testutil.FailErr(t, "seed policy directory", os.Mkdir(path, 0o700))
			case "symlink":
				target := filepath.Join(filepath.Dir(path), "administrator-data")
				testutil.FailErr(t, "seed symlink target", os.WriteFile(target, []byte("preserved"), 0o600))
				testutil.FailErr(t, "seed policy symlink", os.Symlink(target, path))
			case "invalid-path":
				path += "\x00"
			}
			err := withdrawPolicy(path)
			if kind == "missing" {
				testutil.FailErr(t, "withdraw missing selection", err)
				return
			}
			if err == nil {
				t.Fatalf("withdrew %s as the package policy", kind)
			}
			if kind != "invalid-path" {
				_, err := os.Lstat(path)
				testutil.FailErr(t, "preserve refused policy object", err)
			}
			if kind == "symlink" {
				raw, err := os.ReadFile(path)
				testutil.FailErr(t, "preserve symlink target", err)
				if string(raw) != "preserved" {
					t.Fatal("withdrawal followed the refused symlink")
				}
			}
		})
	}
}

// A snapshot fixture exercises the fixed UID0 decision without creating files
// owned by another user. Production always obtains the snapshot through Lstat.
type policyOwnershipSnapshot struct {
	os.FileInfo
	stat any
}

func (s policyOwnershipSnapshot) Sys() any { return s.stat }

func TestWithdrawPolicyRequiresRootOwnershipFacts(t *testing.T) {
	for _, owner := range []struct {
		name    string
		stat    any
		allowed bool
	}{
		{"root", &syscall.Stat_t{Uid: 0}, true},
		{"other-user", &syscall.Stat_t{Uid: 1000}, false},
		{"missing-facts", nil, false},
	} {
		t.Run(owner.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "launch.json")
			testutil.FailErr(t, "seed policy file", os.WriteFile(path, []byte("selected"), 0o600))
			info, err := os.Lstat(path)
			testutil.FailErr(t, "inspect policy fixture", err)
			err = removePolicyFile(path, policyOwnershipSnapshot{FileInfo: info, stat: owner.stat})
			if owner.allowed {
				testutil.FailErr(t, "withdraw root-owned snapshot", err)
				if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("selection remains after withdrawal: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("withdrew policy without root ownership facts")
			}
			raw, err := os.ReadFile(path)
			testutil.FailErr(t, "preserve unowned policy", err)
			if string(raw) != "selected" {
				t.Fatal("ownership refusal changed the policy")
			}
		})
	}
}
