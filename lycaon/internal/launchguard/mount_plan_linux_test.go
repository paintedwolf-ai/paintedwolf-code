//go:build linux

package launchguard

import (
	"bytes"
	"encoding/binary"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func moduleViewPlanFixture() moduleViewPlan {
	return moduleViewPlan{
		source: []byte("10 99 8:1 / / rw shared:1 - ext4 /dev/root rw\n" +
			"11 10 8:2 / /usr ro - ext4 /dev/system ro\n" +
			"102 10 8:1 / /source-only rw - ext4 /dev/root rw\n"),
		staging: []byte("100 99 8:1 / / rw - ext4 /dev/root rw\n" +
			"101 100 8:2 / /usr ro - ext4 /dev/system ro\n" +
			"102 100 0:50 / /media/a\\040b\\377 rw - fuse.test opaque rw\n" +
			"103 102 0:51 / /media/a\\040b\\377/nested rw - tmpfs tmpfs rw\n"),
		structural: []int{11, 10},
		initial: []initialMountLease{
			{lease: 7, mountID: 102, inode: 55},
			{lease: 9, mountID: 103, inode: 55},
		},
	}
}

func encodedModuleViewPlan(t *testing.T, plan moduleViewPlan) []byte {
	t.Helper()
	raw, err := encodeModuleViewPlan(plan)
	testutil.FailErr(t, "encode module-view plan", err)
	return raw
}

func TestModuleViewPlanRoundTripPreservesNamespaceFactsAndOpaqueBytes(t *testing.T) {
	original := moduleViewPlanFixture()
	raw := encodedModuleViewPlan(t, original)
	plan, err := decodeModuleViewPlan(raw)
	testutil.FailErr(t, "decode module-view plan", err)
	if !bytes.Equal(plan.source, original.source) || !bytes.Equal(plan.staging, original.staging) || !slices.Equal(plan.initial, original.initial) || !slices.Equal(plan.structural, []int{10, 11}) {
		t.Fatal("plan changed namespace observations or lease order")
	}
	if !slices.Equal(original.structural, []int{11, 10}) || !bytes.Equal(encodedModuleViewPlan(t, plan), raw) {
		t.Fatal("encoding mutated caller selection or failed canonical round trip")
	}
	entries, err := parseMountTopology(plan.staging)
	testutil.FailErr(t, "read retained staging observations", err)
	if entries[2].mountPoint != "/media/a b\xff" {
		t.Fatalf("opaque mount alias changed: %q", entries[2].mountPoint)
	}
	// Equal numeric IDs across namespaces and equal inodes across different
	// leases are observations, not inferred mount equivalence.
	if plan.initial[0].mountID != 102 || plan.initial[0].inode != plan.initial[1].inode {
		t.Fatal("codec inferred authority from an identity coincidence")
	}
}

func TestModuleViewPlanOwnsDecodedAndEncodedBuffers(t *testing.T) {
	original := moduleViewPlanFixture()
	raw := encodedModuleViewPlan(t, original)
	before := bytes.Clone(raw)
	plan, err := decodeModuleViewPlan(raw)
	testutil.FailErr(t, "decode owned plan", err)
	plan.source[0] = 'x'
	plan.staging[0] = 'y'
	plan.structural[0] = 123
	plan.initial[0].inode = 999
	original.source[0] = 'z'
	if !bytes.Equal(raw, before) {
		t.Fatal("codec returned aliased observation storage")
	}
}

func TestModuleViewPlanAllowsNoInitialVolumesAndMonotonicLeaseGaps(t *testing.T) {
	plan := moduleViewPlanFixture()
	plan.initial = nil
	decoded, err := decodeModuleViewPlan(encodedModuleViewPlan(t, plan))
	testutil.FailErr(t, "decode plan without volumes", err)
	if len(decoded.initial) != 0 {
		t.Fatal("plan invented an initial lease")
	}
	plan.initial = []initialMountLease{{lease: ^uint64(0), mountID: 102, inode: 1}}
	_, err = decodeModuleViewPlan(encodedModuleViewPlan(t, plan))
	testutil.FailErr(t, "retain unsigned lease identity", err)
}

func TestModuleViewPlanRejectsInvalidTopologyAndAuthorityReferences(t *testing.T) {
	cases := map[string]func(*moduleViewPlan){
		"missing root": func(p *moduleViewPlan) { p.structural = []int{11} },
		"missing ancestor": func(p *moduleViewPlan) {
			p.source = append(p.source, []byte("12 11 8:2 /sub /usr/sub ro - ext4 /dev/system ro\n")...)
			p.structural = []int{10, 12}
		},
		"unknown structural":   func(p *moduleViewPlan) { p.structural = []int{10, 999} },
		"duplicate structural": func(p *moduleViewPlan) { p.structural = []int{10, 11, 11} },
		"unknown staging":      func(p *moduleViewPlan) { p.initial[0].mountID = 11 },
		"duplicate staging":    func(p *moduleViewPlan) { p.initial[1].mountID = 102 },
		"zero lease":           func(p *moduleViewPlan) { p.initial[0].lease = 0 },
		"duplicate lease":      func(p *moduleViewPlan) { p.initial[1].lease = 7 },
		"reversed lease":       func(p *moduleViewPlan) { p.initial[1].lease = 6 },
		"zero inode":           func(p *moduleViewPlan) { p.initial[0].inode = 0 },
		"root lease":           func(p *moduleViewPlan) { p.initial[0].mountID = 100 },
		"covered alias": func(p *moduleViewPlan) {
			p.staging = append(p.staging, []byte("104 102 0:52 / /media/a\\040b\\377 rw - tmpfs tmpfs rw\n")...)
			p.initial[1].mountID = 104
		},
		"duplicate source row": func(p *moduleViewPlan) {
			p.source = append(p.source, []byte("10 99 8:1 / / rw - ext4 /dev/root rw\n")...)
		},
		"truncated staging": func(p *moduleViewPlan) { p.staging = p.staging[:len(p.staging)-1] },
		"missing staging parent": func(p *moduleViewPlan) {
			p.staging = bytes.Replace(p.staging, []byte("103 102"), []byte("103 999"), 1)
		},
		"cyclic source": func(p *moduleViewPlan) {
			p.source = bytes.Replace(p.source, []byte("11 10"), []byte("11 11"), 1)
		},
		"empty structural": func(p *moduleViewPlan) { p.structural = nil },
		"empty source":     func(p *moduleViewPlan) { p.source = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			plan := moduleViewPlanFixture()
			change(&plan)
			if _, err := encodeModuleViewPlan(plan); err == nil {
				t.Fatal("accepted invalid mount plan")
			}
		})
	}
}

func TestModuleViewPlanRefusesMalformedBinaryBeforeAdmission(t *testing.T) {
	plan := moduleViewPlanFixture()
	valid := encodedModuleViewPlan(t, plan)
	structural := mountPlanHeaderBytes + len(plan.source) + len(plan.staging)
	initial := structural + 8*len(plan.structural)
	cases := map[string]func([]byte){
		"magic":                     func(raw []byte) { raw[0] = 0 },
		"version":                   func(raw []byte) { binary.LittleEndian.PutUint32(raw[8:], 2) },
		"reserved":                  func(raw []byte) { raw[12] = 1 },
		"source overflow":           func(raw []byte) { binary.LittleEndian.PutUint32(raw[16:], ^uint32(0)) },
		"staging overflow":          func(raw []byte) { binary.LittleEndian.PutUint32(raw[20:], ^uint32(0)) },
		"structural count overflow": func(raw []byte) { binary.LittleEndian.PutUint32(raw[24:], ^uint32(0)) },
		"initial count overflow":    func(raw []byte) { binary.LittleEndian.PutUint32(raw[28:], ^uint32(0)) },
		"zero structural count":     func(raw []byte) { binary.LittleEndian.PutUint32(raw[24:], 0) },
		"oversized mount ID":        func(raw []byte) { binary.LittleEndian.PutUint64(raw[structural:], ^uint64(0)) },
		"unsorted structural": func(raw []byte) {
			binary.LittleEndian.PutUint64(raw[structural:], 11)
			binary.LittleEndian.PutUint64(raw[structural+8:], 10)
		},
		"duplicate structural":       func(raw []byte) { binary.LittleEndian.PutUint64(raw[structural+8:], 10) },
		"zero structural ID":         func(raw []byte) { binary.LittleEndian.PutUint64(raw[structural:], 0) },
		"unknown structural ID":      func(raw []byte) { binary.LittleEndian.PutUint64(raw[structural+8:], 999) },
		"oversized initial mount ID": func(raw []byte) { binary.LittleEndian.PutUint64(raw[initial+8:], ^uint64(0)) },
		"duplicate lease":            func(raw []byte) { binary.LittleEndian.PutUint64(raw[initial+24:], 7) },
		"zero lease":                 func(raw []byte) { binary.LittleEndian.PutUint64(raw[initial:], 0) },
		"root initial lease":         func(raw []byte) { binary.LittleEndian.PutUint64(raw[initial+8:], 100) },
		"duplicate initial ID":       func(raw []byte) { binary.LittleEndian.PutUint64(raw[initial+24+8:], 102) },
		"unknown initial ID":         func(raw []byte) { binary.LittleEndian.PutUint64(raw[initial+8:], 999) },
		"missing inode":              func(raw []byte) { binary.LittleEndian.PutUint64(raw[initial+16:], 0) },
		"malformed source":           func(raw []byte) { raw[mountPlanHeaderBytes] = 'x' },
		"malformed staging":          func(raw []byte) { raw[mountPlanHeaderBytes+len(plan.source)] = 'x' },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			raw := bytes.Clone(valid)
			change(raw)
			if _, err := decodeModuleViewPlan(raw); err == nil {
				t.Fatal("accepted malformed binary mount plan")
			}
		})
	}
	for end := 0; end < len(valid); end++ {
		if _, err := decodeModuleViewPlan(valid[:end]); err == nil {
			t.Fatalf("accepted truncated mount plan at byte %d", end)
		}
	}
	if _, err := decodeModuleViewPlan(append(bytes.Clone(valid), 0)); err == nil {
		t.Fatal("accepted trailing mount plan bytes")
	}
}

func TestModuleViewPlanRefusesOversizedProducerInputs(t *testing.T) {
	for _, which := range []string{"source", "staging", "structural", "initial"} {
		t.Run(which, func(t *testing.T) {
			plan := moduleViewPlanFixture()
			switch which {
			case "source":
				plan.source = make([]byte, maxMountTopologyBytes+1)
			case "staging":
				plan.staging = make([]byte, maxMountTopologyBytes+1)
			case "structural":
				plan.structural = make([]int, maxMountTopologyEntries+1)
			case "initial":
				plan.initial = make([]initialMountLease, maxMountTopologyEntries+1)
			}
			if _, err := encodeModuleViewPlan(plan); err == nil {
				t.Fatal("accepted oversized producer plan")
			}
		})
	}
}
