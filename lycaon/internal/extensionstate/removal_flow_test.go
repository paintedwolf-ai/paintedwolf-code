//go:build integration

package extensionstate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extensionstate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
)

func writeWorkflowCommandPack(t *testing.T, dir, id, workflowID string) {
	t.Helper()
	extpackstest.WriteMinimalPack(t, dir, id, 1)
	extpackstest.MustWrite(t, filepath.Join(dir, "contributions", "commands", "start.yaml"),
		"id: "+id+":start\ntitle: Start\nscope: global\n"+
			"action:\n  kind: workflow_start\n  workflow: "+workflowID+"\n")
}

func TestInstallingABrokenPackStillFails(t *testing.T) {
	owner := testOwner(t)

	broken := filepath.Join(t.TempDir(), "broken")
	writeWorkflowCommandPack(t, broken, "acme/broken", "no-such-workflow")
	_, err := tryApply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + broken})
	if err == nil {
		t.Fatal("installing a pack whose own content does not compile must fail")
	}
	if !strings.Contains(err.Error(), "no-such-workflow") {
		t.Fatalf("error should name the author's own fault: %v", err)
	}
}

func TestRemovingAPackThatIsNotInstalledIsRefused(t *testing.T) {
	owner := testOwner(t)
	_, err := tryApply(t, owner, deviceScope(), extensionstate.RemoveOp{PackID: "acme/never-installed"})
	if err == nil || !strings.Contains(err.Error(), "not a direct device installation") {
		t.Fatalf("want a device-scope refusal, got %v", err)
	}
}

func TestAConfigurationValueTheSchemaRefusesRejectsTheSave(t *testing.T) {
	owner := testOwner(t)
	device := extensionstate.Scope{Kind: "device"}

	pack := filepath.Join(t.TempDir(), "configured")
	extpackstest.WriteMinimalPack(t, pack, "acme/configured", 1)
	extpackstest.MustWrite(t, filepath.Join(pack, "contributions", "configuration", "depth.yaml"),
		"id: acme/configured:depth\ntype: enum\ndescription: How deep.\n"+
			"default: normal\nenum: [shallow, normal, deep]\nscope: [device, project]\n")
	apply(t, owner, device, extensionstate.InstallOp{Source: "path:" + pack})

	_, err := tryApply(t, owner, device, extensionstate.SetConfigurationOp{
		Packs: map[string]map[string]any{"acme/configured": {"depth": "turbo"}},
	})
	if err == nil {
		t.Fatal("a value outside the declared enum must reject the save")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Fatalf("error should name the property: %v", err)
	}
}

func TestReviewedRemovalRejectsWholeBatchBeforePublication(t *testing.T) {
	owner := testOwner(t)
	first, second := filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")
	extpackstest.WriteMinimalPack(t, first, "acme/one", 1)
	extpackstest.WriteMinimalPack(t, second, "acme/two", 1)
	testutil.FailErr(t, "remove shared policy fixture", os.Remove(filepath.Join(second, "policy", "ACME_HELLO.yaml")))
	extpackstest.MustWrite(t, filepath.Join(second, "policy", "ACME_TWO.yaml"), "id: ACME_TWO\nemit: banner\nmessage: hi\neffect: warn\n")
	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + first})
	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + second})
	before, err := owner.RemovalState()
	testutil.FailErr(t, "read before", err)
	_, err = tryApply(t, owner, deviceScope(), extensionstate.RemoveReviewedOp{
		PackIDs:    []string{"acme/one", "acme/two"},
		Revalidate: func(context.Context) error { return errors.New("reference changed") },
	})
	if err == nil {
		t.Fatal("changed references admitted")
	}
	after, err := owner.RemovalState()
	testutil.FailErr(t, "read after", err)
	if before.Revision != after.Revision {
		t.Fatal("failed batch changed extension state")
	}
}

func TestReviewedRemovalDoesNotDisableBrokenRemainingPack(t *testing.T) {
	owner := testOwner(t)
	provider, consumer := filepath.Join(t.TempDir(), "provider"), filepath.Join(t.TempDir(), "consumer")
	extpackstest.WriteMinimalPack(t, provider, "acme/provider", 1)
	extpackstest.WriteMinimalPack(t, consumer, "acme/consumer", 1)
	testutil.FailErr(t, "remove shared policy fixture", os.Remove(filepath.Join(consumer, "policy", "ACME_HELLO.yaml")))
	extpackstest.MustWrite(t, filepath.Join(consumer, "policy", "ACME_CONSUMER.yaml"),
		"id: ACME_CONSUMER\nemit: banner\nmessage: hi\neffect: warn\n")
	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + provider})
	apply(t, owner, deviceScope(), extensionstate.InstallOp{Source: "path:" + consumer})
	// A linked pack can become invalid after installation.
	extpackstest.MustWrite(t, filepath.Join(consumer, "contributions", "commands", "start.yaml"),
		"id: acme/consumer:start\ntitle: Start\nscope: global\n"+
			"action: {kind: workflow_start, workflow: missing-workflow}\n")
	before, err := owner.RemovalState()
	testutil.FailErr(t, "read before", err)
	_, err = tryApply(t, owner, deviceScope(), extensionstate.RemoveReviewedOp{
		PackIDs: []string{"acme/provider"}, Revalidate: func(context.Context) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "would disable other extensions") {
		t.Fatalf("removal must refuse disabling the remaining pack: %v", err)
	}
	after, err := owner.RemovalState()
	testutil.FailErr(t, "read after", err)
	if before.Revision != after.Revision {
		t.Fatal("rejected removal changed extension state")
	}
}
