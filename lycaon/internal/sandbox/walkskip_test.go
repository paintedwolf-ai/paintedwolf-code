package sandbox

import (
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"testing"
)

func TestShouldSkipDirBaseName(t *testing.T) {
	if !ShouldSkipDirBaseName(settingsoverlay.DirName()) || !ShouldSkipDirBaseName(".git") {
		t.Fatal("expected engine and VCS skip dir base names")
	}
	if ShouldSkipDirBaseName("node_modules") || ShouldSkipDirBaseName("target") {
		t.Fatal("stack-specific dirs must not be in the skip set")
	}
	if ShouldSkipDirBaseName("plans") || ShouldSkipDirBaseName("src") {
		t.Fatal("non-skip names must not match base-name check")
	}
}

func TestShouldSkipDirSubtreePrefix(t *testing.T) {
	if !ShouldSkipDir(settingsoverlay.DirName()+"/plans", "plans") {
		t.Fatal("descendant of the overlay must be pruned on recursive walks")
	}
	if ShouldSkipDirBaseName("plans") {
		t.Fatal("base-name check must not use ancestor prefix paths")
	}
}
