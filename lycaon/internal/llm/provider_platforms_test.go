package llm

import (
	"testing"
)

func TestPlatformsSupported(t *testing.T) {
	if !PlatformsSupported(nil, PlatformLinux) {
		t.Fatal("empty platforms should allow all hosts")
	}
	if !PlatformsSupported([]string{PlatformmacOS}, PlatformmacOS) {
		t.Fatal("macos should match")
	}
	if PlatformsSupported([]string{PlatformmacOS}, PlatformLinux) {
		t.Fatal("macos-only should reject linux")
	}
}

func TestHostProductPlatformOverride(t *testing.T) {
	t.Setenv("LYCAON_TEST", "1")
	t.Cleanup(SetHostProductPlatformForTest(PlatformWindows))
	if HostProductPlatform() != PlatformWindows {
		t.Fatalf("HostProductPlatform = %q", HostProductPlatform())
	}
	if !PlatformsSupportedHere([]string{PlatformWindows}) {
		t.Fatal("expected windows supported here")
	}
}
