package extensionstate_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

func TestCredentialExtensionTransactions(t *testing.T) {
	owner := testOwner(t)
	root := filepath.Join(t.TempDir(), "recognition")
	extpackstest.MustWrite(t, filepath.Join(root, "extension.yaml"), "manifest_version: 1\nid: acme/recognition\nname: Recognition\nversion: 1.0.0\ncompatibility:\n  extension_api: ^1.0.0\n  requires_capabilities: [host.credential_slots]\ndependencies:\n  painted-wolf/platform:\n    version: ^1.0.0\n")
	path := filepath.Join(root, "host", "credential-slots", "access.yaml")
	extpackstest.MustWrite(t, path, "version: 1\nenv_keys: [ACCESS_VALUE]")
	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + root})
	assertRecognition := func(want int) {
		t.Helper()
		eff, err := extpacks.ResolveCatalog(t.Context(), nil, nil)
		testutil.FailErr(t, "resolve committed recognition", err)
		view, err := owner.Views.For(t.Context(), eff)
		testutil.FailErr(t, "compile committed recognition", err)
		if hits := view.CredentialSlots.Inspect("external", map[string]any{"ACCESS_VALUE": "one", "NEW_VALUE": "two", "password": "three"}); len(hits) != want {
			t.Fatalf("committed recognition count = %d, want %d", len(hits), want)
		}
	}
	assertRecognition(2)
	const id = "host/credential-slots/acme/recognition:access"
	apply(t, owner, deviceScope(), extensionstate.SetUnitDisabledOp{UnitID: id, Disabled: true})
	assertRecognition(1)
	apply(t, owner, deviceScope(), extensionstate.SetUnitDisabledOp{UnitID: id, Disabled: false})
	assertRecognition(2)
	desiredBefore, err := os.ReadFile(mustDeviceDesiredPath(t))
	testutil.FailErr(t, "read intent before invalid reload", err)
	lockBefore, err := os.ReadFile(mustDeviceLockPath(t))
	testutil.FailErr(t, "read lock before invalid reload", err)
	extpackstest.MustWrite(t, path, "version: 1\nexcluded_keys: [password]")
	if _, err := tryApply(t, owner, deviceScope(), extensionstate.ReloadOp{PackID: "acme/recognition"}); err == nil {
		t.Fatal("invalid recognition reload was published")
	}
	desiredAfter, err := os.ReadFile(mustDeviceDesiredPath(t))
	testutil.FailErr(t, "read intent after invalid reload", err)
	lockAfter, err := os.ReadFile(mustDeviceLockPath(t))
	testutil.FailErr(t, "read lock after invalid reload", err)
	if !bytes.Equal(desiredBefore, desiredAfter) || !bytes.Equal(lockBefore, lockAfter) {
		t.Fatal("rejected reload changed committed intent or lock")
	}
	extpackstest.MustWrite(t, path, "version: 1\nenv_keys: [ACCESS_VALUE, NEW_VALUE]")
	apply(t, owner, deviceScope(), extensionstate.ReloadOp{PackID: "acme/recognition"})
	assertRecognition(3)
	apply(t, owner, deviceScope(), extensionstate.RemoveOp{PackID: "acme/recognition"})
	assertRecognition(1)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("removal changed the author's linked pack: %v", err)
	}
}

func TestCredentialProjectDisableRejectsBeforePublication(t *testing.T) {
	owner := testOwner(t)
	root := writeAuthorPack(t, "acme/recognition", map[string]string{
		"host/credential-slots/access.yaml": "version: 1\nenv_keys: [ACCESS_VALUE]",
	})
	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + root})
	projectDir := t.TempDir()
	scope := extensionstate.Scope{Kind: "project", ProjectID: "probe", ProjectDir: projectDir}
	const id = "host/credential-slots/acme/recognition:access"
	before := currentRevision(t, owner, projectDir)
	_, err := tryApply(t, owner, scope, extensionstate.SetUnitDisabledOp{UnitID: id, Disabled: true})
	if !errors.Is(err, extensionstate.ErrProjectUnitDisableUnsupported) {
		t.Fatalf("project disable = %v, want scope refusal", err)
	}
	if currentRevision(t, owner, projectDir) != before {
		t.Fatal("refused project disable published a revision")
	}
	path := extpacks.ProjectDesiredPath(projectDir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("refused project disable wrote desired state: %v", err)
	}
	// Ignored project entries remain removable through the owner.
	suggestion := extpacks.EmptySuggestion()
	suggestion.Disabled = []string{id}
	body, err := extpacks.EncodeSuggestion(suggestion)
	testutil.FailErr(t, "encode ignored project entry", err)
	extpackstest.MustWrite(t, path, string(body))
	result := apply(t, owner, scope, extensionstate.SetUnitDisabledOp{UnitID: id, Disabled: false})
	if len(result.Desired.Disabled) != 0 {
		t.Fatal("reenabling failed to remove ignored project entry")
	}
}
