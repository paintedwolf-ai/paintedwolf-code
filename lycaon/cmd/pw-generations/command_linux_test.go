//go:build linux

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/packagegeneration"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInstallDispatchSelectsTheRequestedFamilyAndReportsItsGeneration(t *testing.T) {
	for _, family := range []string{"deb", "rpm", "pacman"} {
		t.Run(family, func(t *testing.T) {
			policy := filepath.Join(t.TempDir(), "policy", "launch.json")
			generation := packagegeneration.Generation{Root: "/immutable/generation", ManifestSHA256: strings.Repeat("a", 64)}
			var output bytes.Buffer
			install := func(got string, commit func(packagegeneration.Generation, packagegeneration.SystemAccess) error, selected func() (string, error)) (packagegeneration.Generation, error) {
				if got != family || commit == nil || selected == nil {
					t.Fatalf("install owner received family=%q commit=%v selection=%v", got, commit != nil, selected != nil)
				}
				info, err := os.Stat(filepath.Dir(policy))
				testutil.FailErr(t, "policy parent exists before installation", err)
				if !info.IsDir() {
					t.Fatal("installation started before the policy parent was prepared")
				}
				return generation, nil
			}
			testutil.FailErr(t, "dispatch package installation", runCommand([]string{"install", "--family", family}, &output, policy, install, refuseRemoval(t)))
			if got, want := output.String(), "painted-wolf-code: selected generation "+generation.ManifestSHA256+"\n"; got != want {
				t.Fatalf("installation report = %q, want %q", got, want)
			}
		})
	}
}

func TestInstallFailureNeverReportsSelection(t *testing.T) {
	cause := errors.New("sealed launch catalog is missing")
	var output bytes.Buffer
	install := func(string, func(packagegeneration.Generation, packagegeneration.SystemAccess) error, func() (string, error)) (packagegeneration.Generation, error) {
		return packagegeneration.Generation{}, cause
	}
	err := runCommand([]string{"install", "--family", "deb"}, &output, filepath.Join(t.TempDir(), "launch.json"), install, refuseRemoval(t))
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "could not install the protected launch generation") || output.Len() != 0 {
		t.Fatalf("failed installation = %v, report=%q", err, output.String())
	}
}

func TestPolicyParentFailureDoesNotEnterTheGenerationOwner(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	testutil.FailErr(t, "seed obstructed policy parent", os.WriteFile(parent, []byte("unchanged"), 0o600))
	err := runCommand([]string{"install", "--family", "rpm"}, io.Discard, filepath.Join(parent, "launch.json"), refuseInstallation(t), refuseRemoval(t))
	if err == nil {
		t.Fatal("obstructed policy parent admitted installation")
	}
	contents, err := os.ReadFile(parent)
	testutil.FailErr(t, "read obstructed parent", err)
	if string(contents) != "unchanged" {
		t.Fatal("installation changed the obstructing file")
	}
}

func TestRemoveDispatchPreservesOwnerFailure(t *testing.T) {
	for _, cause := range []error{nil, errors.New("generation remains held")} {
		var output bytes.Buffer
		remove := func(withdraw func() error, selected func() (string, error)) error {
			if withdraw == nil || selected == nil {
				t.Fatal("removal omitted its policy callbacks")
			}
			return cause
		}
		err := runCommand([]string{"remove"}, &output, "", refuseInstallation(t), remove)
		if !errors.Is(err, cause) || output.Len() != 0 {
			t.Fatalf("removal = %v, want %v without install report; report=%q", err, cause, output.String())
		}
	}
}

func TestInvalidOperationsNeverEnterGenerationOwners(t *testing.T) {
	for _, args := range [][]string{nil, {"install", "--family", "dmg"}, {"install", "--family", "deb", "extra"}, {"remove", "--all"}} {
		if err := runCommand(args, io.Discard, "", refuseInstallation(t), refuseRemoval(t)); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Fatalf("invalid operation %q = %v", args, err)
		}
	}
}

func refuseInstallation(t *testing.T) func(string, func(packagegeneration.Generation, packagegeneration.SystemAccess) error, func() (string, error)) (packagegeneration.Generation, error) {
	t.Helper()
	return func(string, func(packagegeneration.Generation, packagegeneration.SystemAccess) error, func() (string, error)) (packagegeneration.Generation, error) {
		t.Fatal("unexpected installation")
		return packagegeneration.Generation{}, errors.New("unexpected installation")
	}
}

func refuseRemoval(t *testing.T) func(func() error, func() (string, error)) error {
	t.Helper()
	return func(func() error, func() (string, error)) error {
		t.Fatal("unexpected removal")
		return errors.New("unexpected removal")
	}
}
