package gotreesitter

import (
	"bytes"
	"reflect"
	"testing"
)

// TestLanguageHasExactlyOneExportedMapField guards the trailer mechanism's
// core assumption: LargeStateGotos is the ONLY exported map field on Language.
// gob encodes map iteration in randomized order, so any exported map field
// that is gob-encoded directly reintroduces blob nondeterminism. If this test
// fails because a new exported map field was added, route that field through
// a sorted trailer too (see large_state_gotos_trailer.go) or use a
// deterministic slice representation instead.
func TestLanguageHasExactlyOneExportedMapField(t *testing.T) {
	lt := reflect.TypeOf(Language{})
	var mapFields []string
	for i := 0; i < lt.NumField(); i++ {
		f := lt.Field(i)
		if f.IsExported() && f.Type.Kind() == reflect.Map {
			mapFields = append(mapFields, f.Name)
		}
	}
	if len(mapFields) != 1 || mapFields[0] != "LargeStateGotos" {
		t.Fatalf("Language exported map fields = %v, want exactly [LargeStateGotos]; "+
			"a directly gob-encoded map field makes blob encoding nondeterministic "+
			"(see large_state_gotos_trailer.go)", mapFields)
	}
}

// buildSyntheticLargeStateGotos returns a map shaped like what
// grammargen/assemble.go's recordLargeGoto populates: keys are
// uint64(state)<<32 | uint64(symbol), values are StateID targets. n entries
// is enough to make Go's randomized map iteration produce different orders
// across repeated ranges with overwhelming probability.
func buildSyntheticLargeStateGotos(n int) map[uint64]StateID {
	m := make(map[uint64]StateID, n)
	for i := 0; i < n; i++ {
		state := uint64(70000 + i)
		sym := uint64(i % 512)
		key := state<<32 | sym
		m[key] = StateID(80000 + i*3)
	}
	return m
}

func TestEncodeLargeStateGotosTrailerDeterministicAcrossRuns(t *testing.T) {
	m := buildSyntheticLargeStateGotos(5000)

	first, err := EncodeLargeStateGotosTrailer(m)
	if err != nil {
		t.Fatalf("EncodeLargeStateGotosTrailer: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("expected non-empty trailer for non-empty map")
	}

	for i := 0; i < 25; i++ {
		got, err := EncodeLargeStateGotosTrailer(m)
		if err != nil {
			t.Fatalf("EncodeLargeStateGotosTrailer run %d: %v", i, err)
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("run %d: trailer bytes differ from first run (gob map-iteration nondeterminism leaked through)", i)
		}
	}
}

func TestLargeStateGotosTrailerRoundTrip(t *testing.T) {
	m := buildSyntheticLargeStateGotos(500)

	enc, err := EncodeLargeStateGotosTrailer(m)
	if err != nil {
		t.Fatalf("EncodeLargeStateGotosTrailer: %v", err)
	}

	got, err := DecodeLargeStateGotosTrailer(bytes.NewReader(enc))
	if err != nil {
		t.Fatalf("DecodeLargeStateGotosTrailer: %v", err)
	}
	if len(got) != len(m) {
		t.Fatalf("decoded map has %d entries, want %d", len(got), len(m))
	}
	for k, want := range m {
		if got[k] != want {
			t.Fatalf("decoded[%d] = %d, want %d", k, got[k], want)
		}
	}
}

func TestEncodeLargeStateGotosTrailerEmptyMapReturnsNil(t *testing.T) {
	for name, m := range map[string]map[uint64]StateID{
		"nil map":   nil,
		"empty map": {},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := EncodeLargeStateGotosTrailer(m)
			if err != nil {
				t.Fatalf("EncodeLargeStateGotosTrailer: %v", err)
			}
			if got != nil {
				t.Fatalf("expected nil trailer for %s, got %d bytes", name, len(got))
			}
		})
	}
}

func TestDecodeLargeStateGotosTrailerEmptyReaderReturnsNil(t *testing.T) {
	for name, r := range map[string]*bytes.Reader{
		"nil reader":   nil,
		"empty reader": bytes.NewReader(nil),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeLargeStateGotosTrailer(r)
			if err != nil {
				t.Fatalf("DecodeLargeStateGotosTrailer: %v", err)
			}
			if got != nil {
				t.Fatalf("expected nil map for %s, got %v", name, got)
			}
		})
	}
}

func TestDecodeLargeStateGotosTrailerRejectsGarbage(t *testing.T) {
	garbage := bytes.NewReader([]byte{0xFF, 0x00, 0x01, 0x02, 0x03})
	if _, err := DecodeLargeStateGotosTrailer(garbage); err == nil {
		t.Fatal("expected an error decoding non-gob garbage bytes, got nil")
	}
}

func TestDecodeLargeStateGotosTrailerRejectsTrailingBytes(t *testing.T) {
	encoded, err := EncodeLargeStateGotosTrailer(buildSyntheticLargeStateGotos(3))
	if err != nil {
		t.Fatalf("EncodeLargeStateGotosTrailer: %v", err)
	}
	encoded = append(encoded, 0xde, 0xad, 0xbe, 0xef)
	if _, err := DecodeLargeStateGotosTrailer(bytes.NewReader(encoded)); err == nil {
		t.Fatal("expected bytes after the trailer gob to be rejected")
	}
}
