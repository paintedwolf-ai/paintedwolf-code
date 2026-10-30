//go:build linux

package launchguard

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTopologyRefusalKeepsExactClosedPredicateAndPrivateStatus(t *testing.T) {
	root := "20 1 8:1 / / rw - ext4 root rw\n"
	cases := []struct {
		raw               string
		predicate         topologyPredicate
		id, parent, roots int
	}{
		{"", topologyTruncated, 0, 0, 0},
		{"invalid\n", topologyInvalidRecord, 0, 0, 0},
		{root + root, topologyDuplicateIdentity, 20, 1, 0},
		{root + "2 88 8:2 / /volume rw - ext4 root rw\n", topologyMissingParent, 2, 88, 0},
		{root + "2 88 8:2 / / rw - ext4 root rw\n", topologyAmbiguousRoot, 0, 0, 2},
		{root + "2 3 8:2 / /a rw - ext4 root rw\n3 2 8:3 / /b rw - ext4 root rw\n", topologyCycle, 2, 3, 0},
	}
	for _, test := range cases {
		_, err := parseMountTopology([]byte(test.raw))
		var refusal *mountTopologyRefusal
		if !errors.As(err, &refusal) || refusal.predicate != test.predicate || refusal.bytes != len(test.raw) ||
			refusal.id != test.id || refusal.parent != test.parent || refusal.roots != test.roots {
			t.Fatalf("closed topology predicate lost actual scalar facts: got=%+v want=%+v", refusal, test)
		}
		if status := MountIntakeExitStatus(&intakeMonitorFailure{intakeFailureTopology, err}); status != 100+int(test.predicate) {
			t.Fatalf("private topology status lost predicate: %d", status)
		}
		if MountIntakeExitStatus(err) != 1 || MountIntakeExitStatus(&intakeMonitorFailure{intakeFailureReconcile, err}) != 110+int(test.predicate) ||
			MountIntakeExitStatus(&intakeMonitorFailure{intakeFailureHeartbeat, err}) != 85 {
			t.Fatal("parser diagnostic changed another role or failure phase")
		}
	}
	if MountIntakeExitStatus(&intakeMonitorFailure{intakeFailureTopology, unix.EIO}) != 83 ||
		MountIntakeExitStatus(&intakeMonitorFailure{intakeFailureTopology, &mountTopologyRefusal{predicate: 255}}) != 83 {
		t.Fatal("unknown topology cause gained a fabricated subtype")
	}
	if MountIntakeExitStatus(&intakeMonitorFailure{intakeFailureReconcile, unix.EIO}) != 84 ||
		MountIntakeExitStatus(&intakeMonitorFailure{intakeFailureReconcile, &mountTopologyRefusal{predicate: 255}}) != 84 {
		t.Fatal("unknown reconcile cause gained a fabricated topology subtype")
	}
	if _, err := parseMountTopology([]byte(root)); err != nil {
		t.Fatalf("valid complete topology changed admission: %v", err)
	}
}
