//go:build linux

package launchguard

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMountTopologyKeepsKernelIdentityAndRejectsIncompleteGraphs(t *testing.T) {
	const root = "20 1 8:1 / / rw,noexec,relatime shared:4 - ext4 /dev/root rw\n"
	const volume = "21 20 0:25 /subtree /media/USB\\040Drive\\012line rw,nosuid master:5 propagate_from:3 - fuse.test source\\040name rw,user_id=1000\n"
	entries, err := parseMountTopology([]byte(root + volume))
	testutil.FailErr(t, "decode complete kernel topology", err)
	got := entries[1]
	if got.id != 21 || got.parent != 20 || got.device != "0:25" || got.root != "/subtree" || got.mountPoint != "/media/USB Drive\nline" || got.filesystem != "fuse.test" || got.source != "source name" || got.master != 5 || got.propagateFrom != 3 || got.superOptions != "rw,user_id=1000" {
		t.Fatalf("lost mount facts: %+v", got)
	}
	for name, raw := range map[string]string{
		"truncated":             strings.TrimSuffix(root, "\n"),
		"malformed":             root + "bad line\n",
		"duplicate":             root + volume + volume,
		"missing parent":        root + strings.Replace(volume, "21 20", "21 99", 1),
		"cycle":                 root + strings.Replace(volume, "21 20", "21 21", 1),
		"unknown propagation":   root + strings.Replace(volume, "master:5", "other:5", 1),
		"duplicate propagation": root + strings.Replace(volume, "master:5", "master:5 master:5", 1),
		"malformed escape":      root + strings.Replace(volume, "USB\\040", "USB\\400", 1),
		"embedded nul":          root + strings.Replace(volume, "USB\\040", "USB\\000", 1),
		"duplicate options":     strings.Replace(root, "rw,noexec", "rw,rw,noexec", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMountTopology([]byte(raw)); err == nil {
				t.Fatal("accepted incomplete or ambiguous mount topology")
			}
		})
	}

}

func TestMountTopologyDeepChainAndAdmissionBounds(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("2 1 8:1 / / rw - ext4 /dev/root rw\n")
	for id := 3; id <= maxMountTopologyEntries+1; id++ {
		fmt.Fprintf(&raw, "%d %d 8:1 / /nested rw - ext4 /dev/root rw\n", id, id-1)
	}
	entries, err := parseMountTopology([]byte(raw.String()))
	testutil.FailErr(t, "decode maximum-depth topology", err)
	if len(entries) != maxMountTopologyEntries {
		t.Fatalf("decoded %d entries", len(entries))
	}
	if _, err := parseMountTopology([]byte(raw.String() + "32770 32769 8:1 / /extra rw - ext4 /dev/root rw\n")); err == nil {
		t.Fatal("accepted too many mount entries")
	}
	if _, err := parseMountTopology([]byte(strings.Repeat("x", maxMountTopologyBytes) + "\n")); err == nil {
		t.Fatal("accepted oversized topology")
	}
	cycle := strings.Replace(raw.String(), "2 1 8:1", fmt.Sprintf("2 %d 8:1", maxMountTopologyEntries+1), 1)
	if _, err := parseMountTopology([]byte(cycle)); err == nil {
		t.Fatal("accepted a deep cycle")
	}
}

func TestMountTopologyPreservesSyntheticKernelRoots(t *testing.T) {
	raw := "20 1 8:1 / / rw - ext4 /dev/root rw\n21 20 0:4 net:[4026531993] /namespace-handle rw - nsfs nsfs rw\n"
	entries, err := parseMountTopology([]byte(raw))
	testutil.FailErr(t, "decode an nsfs namespace bind", err)
	if entries[1].root != "net:[4026531993]" || entries[1].mountPoint != "/namespace-handle" {
		t.Fatalf("synthetic root identity changed: %+v", entries[1])
	}
	if _, err := parseMountTopology([]byte(strings.Replace(raw, "/namespace-handle", "namespace-handle", 1))); err == nil {
		t.Fatal("accepted a relative attachment point")
	}
}
