package browser

import (
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseChordReadsModifiersThenOneKey(t *testing.T) {
	mod := "Control"
	if runtime.GOOS == "darwin" {
		mod = "Meta"
	}
	cases := []struct {
		chord     string
		modifiers []string
		key, code string
	}{
		{"Enter", nil, "\r", "Enter"},
		{"a", nil, "a", "KeyA"},
		{"Shift+Tab", []string{"Shift"}, "\t", "Tab"},
		{"Mod+Shift+K", []string{mod, "Shift"}, "K", "KeyK"},
		{"ctrl+ArrowDown", []string{"Control"}, "ArrowDown", "ArrowDown"},
		{"Cmd++", []string{"Meta"}, "+", "NumpadAdd"},
		{" Cmd++ ", []string{"Meta"}, "+", "NumpadAdd"},
		{"+", nil, "+", "NumpadAdd"},
		{"F5", nil, "F5", "F5"},
		{"é", nil, "é", ""},
	}
	for _, tc := range cases {
		stroke, err := parseChord(tc.chord)
		if err != nil {
			t.Fatalf("parseChord(%q): %v", tc.chord, err)
		}
		var names []string
		for _, m := range stroke.modifiers {
			names = append(names, m.name)
		}
		if len(names) != len(tc.modifiers) || stroke.key.Key != tc.key || stroke.key.Code != tc.code {
			t.Fatalf("parseChord(%q) = modifiers %v key %q code %q; want %v %q %q", tc.chord, names, stroke.key.Key, stroke.key.Code, tc.modifiers, tc.key, tc.code)
		}
		for i := range names {
			if names[i] != tc.modifiers[i] {
				t.Fatalf("parseChord(%q) modifiers %v, want %v", tc.chord, names, tc.modifiers)
			}
		}
	}
}

func TestParseChordRejectsWhatItCannotPress(t *testing.T) {
	for _, chord := range []string{"", "Hyper+K", "Mod+", "Shift+NotAKey"} {
		if _, err := parseChord(chord); err == nil {
			t.Fatalf("parseChord(%q) accepted an unpressable chord", chord)
		}
	}
}

func TestEditingCommandsFollowTheModifierOrderOfTheCommandTable(t *testing.T) {
	stroke, err := parseChord("Meta+Shift+z")
	testutil.FailErr(t, "parseChord failed", err)
	if got := editingCommandKey(stroke); got != "Shift+Meta+KeyZ" {
		t.Fatalf("editing command key = %q", got)
	}
	if macEditingCommands[editingCommandKey(stroke)][0] != "redo" {
		t.Fatalf("Shift+Meta+Z does not redo")
	}
}
