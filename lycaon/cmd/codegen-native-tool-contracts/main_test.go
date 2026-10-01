package main

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCapabilitiesConstantIncludesLoopbackConnect(t *testing.T) {
	got, err := capabilitiesConstant([]string{
		"host_resource", "socket", "direct_ip", "local_listen", "loopback_connect", "terminal_capture", "write_root",
	})
	testutil.FailErr(t, "capabilitiesConstant", err)
	for _, want := range []string{
		"CapabilityHostResource", "CapabilitySocket", "CapabilityDirectIP",
		"CapabilityLocalListen", "CapabilityLoopbackConnect", "CapabilityTerminalCapture", "CapabilityWriteRoot",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %q", want, got)
		}
	}
}

func TestCapabilitiesConstantUnknownFailsClosed(t *testing.T) {
	_, err := capabilitiesConstant([]string{"loopback_connect", "not_a_capability"})
	if err == nil || !strings.Contains(err.Error(), "not_a_capability") {
		t.Fatalf("unknown capability must fail closed, got %v", err)
	}
}

func TestCapabilitiesConstantEmptyIsZero(t *testing.T) {
	got, err := capabilitiesConstant(nil)
	testutil.FailErr(t, "capabilitiesConstant empty", err)
	if got != "0" {
		t.Fatalf("empty = %q", got)
	}
}
