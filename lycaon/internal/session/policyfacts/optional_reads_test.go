package policyfacts

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
)

func TestOptionalProtectedReadsAppearOncePerPathAndChat(t *testing.T) {
	m := New(nil)
	facts := guidance.ToolResultFacts{
		CommandExitedZero: true,
		Confine: confine.Observation{Applied: true, Refusals: confine.SandboxRefusals{Refusals: []confine.SandboxRefusal{
			{Target: "/fixture/preferences", Recovery: confine.RecoverReadPath},
			{Target: "/fixture/preferences", Recovery: confine.RecoverReadPath},
			{Target: "/fixture/defaults", Recovery: confine.RecoverReadPath},
		}}},
	}
	want := []string{"/fixture/preferences", "/fixture/defaults"}
	otherRefusal := facts
	otherRefusal.Confine.Destination = "fixture.invalid"
	if paths := m.optionalReads.observe("first", otherRefusal); len(paths) != 0 {
		t.Fatalf("mixed refusals produced an optional notice: %v", paths)
	}
	if paths := m.optionalReads.observe("first", facts); !slices.Equal(paths, want) {
		t.Fatalf("first notice = %v, want %v", paths, want)
	}
	if paths := m.optionalReads.observe("first", facts); len(paths) != 0 {
		t.Fatalf("second notice repeated %v", paths)
	}
	if paths := m.optionalReads.observe("other", facts); !slices.Equal(paths, want) {
		t.Fatalf("other chat's notice = %v, want %v", paths, want)
	}
	m.ForgetSession("first")
	if paths := m.optionalReads.observe("first", facts); !slices.Equal(paths, want) {
		t.Fatalf("released chat's notice = %v, want %v", paths, want)
	}
}
