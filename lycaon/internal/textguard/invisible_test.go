package textguard

import "testing"

func TestInvisibleFormatRuneCoversSmugglingSets(t *testing.T) {
	for _, r := range []rune{0x200B, 0x200C, 0x200D, 0xFEFF, 0x2060, 0x202E, 0x2066, 0xE0001, 0xE007F, 0x00} {
		if !InvisibleFormatRune(r) {
			t.Errorf("InvisibleFormatRune(%#U) = false, want true", r)
		}
	}
}

// Tabs and newlines are layout in a governance file, not smuggling.
func TestInvisibleFormatRunePreservesLayoutAndText(t *testing.T) {
	for _, r := range []rune{'a', 'Z', '9', ' ', '\t', '\n', '—', '好', '🙂'} {
		if InvisibleFormatRune(r) {
			t.Errorf("InvisibleFormatRune(%#U) = true, want false", r)
		}
	}
}

func TestStripUntilStableRemovesNestedSmuggling(t *testing.T) {
	// A zero-width run interleaved so a single pass would leave a second layer.
	in := "run\u200b\u200b rm\u202e -rf\ufeff /"
	got := StripInvisibleFormatRunesUntilStable(in)
	want := "run rm -rf /"
	if got != want {
		t.Fatalf("stripped = %q, want %q", got, want)
	}
}

func TestStripUntilStableKeepsTabsAndNewlines(t *testing.T) {
	in := "line one\n\tindented\n"
	if got := StripInvisibleFormatRunesUntilStable(in); got != in {
		t.Fatalf("stripped = %q, want unchanged %q", got, in)
	}
}

func TestStripUntilStableIdempotent(t *testing.T) {
	in := "a\u200bb\u202ec"
	once := StripInvisibleFormatRunesUntilStable(in)
	if twice := StripInvisibleFormatRunesUntilStable(once); twice != once {
		t.Fatalf("not idempotent: %q then %q", once, twice)
	}
}
