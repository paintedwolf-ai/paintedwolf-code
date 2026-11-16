package secretmatch

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

// secretAlphabet spells planted values from runes no marker or reference
// contains, so a planted value in the output can only be leaked input.
const secretAlphabet = "ghjkmquvxyzBFGHIJKLMNOPQSUVWXYZ _\nλ🙂"

// chunkReader returns the input in the sizes a schedule names: each byte is
// one read of 1 to 64 bytes, or an empty read; the schedule repeats.
type chunkReader struct {
	data     []byte
	schedule []byte
	step     int
	emptied  bool
	eofEarly bool
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	size := 64
	if len(r.schedule) > 0 {
		b := r.schedule[r.step%len(r.schedule)]
		r.step++
		if b == 0 && !r.emptied {
			r.emptied = true
			return 0, nil
		}
		size = int(b)%64 + 1
	}
	r.emptied = false
	n := copy(p, r.data[:min(size, len(r.data))])
	r.data = r.data[n:]
	if r.eofEarly && len(r.data) == 0 {
		return n, io.EOF
	}
	return n, nil
}

func plantedSecret(seed []byte) string {
	alphabet := []rune(secretAlphabet)
	var b strings.Builder
	for _, c := range seed[:min(len(seed), 48)] {
		b.WriteRune(alphabet[int(c)%len(alphabet)])
	}
	return b.String()
}

// FuzzScreenBytesRedactsAcrossChunkSplits plants a managed value in arbitrary
// surrounding bytes, reads it through an arbitrary split and window profile,
// and requires that the value never survives and that the split changes
// nothing: the stream equals the whole-string screen, or with managed echoes,
// the unsplit stream.
func FuzzScreenBytesRedactsAcrossChunkSplits(f *testing.F) {
	f.Add([]byte("orchard-value"), []byte("before: "), []byte(" :after\n"), []byte{1, 7, 0, 63}, uint16(0), false, false)
	f.Add([]byte("deploy token value"), bytes.Repeat([]byte("."), 300), bytes.Repeat([]byte("λ"), 90), []byte{3}, uint16(511), true, true)
	f.Add([]byte("\x05\x09\x0c"), []byte("line one\n"), []byte("\nline two"), []byte{0, 0, 2}, uint16(64), true, false)
	f.Fuzz(func(t *testing.T, seed, before, after, schedule []byte, profile uint16, echoes, eofEarly bool) {
		secret := plantedSecret(seed)
		// Redaction keeps line breaks, so a value that starts or ends with one could
		// re-form from a kept break and the following bytes.
		if utf8.RuneCountInString(secret) < MinManagedSecretRunes || strings.TrimSpace(secret) != secret || len(before)+len(after) > 4096 {
			return
		}
		overlap := referenceContextBytes + int(profile%64)
		window := overlap + 1 + int(profile>>6)
		m := NewInertMatcher()
		m.SetScreenWindow(window, overlap)
		m.SetHarvestSource(func(context.Context) []HarvestedValue {
			return []HarvestedValue{managedValue(secret, testReference)}
		})
		input := string(before) + secret + string(after)
		policy := StreamPolicy{ManagedEchoes: echoes}
		screen := func(r io.Reader) string {
			var out bytes.Buffer
			_, err := m.ScreenBytes(t.Context(), r, &out, "", policy, StreamBounds{InputBytes: 1 << 20, OutputBytes: 1 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 36}, nil, nil, nil)
			if err != nil {
				t.Fatalf("screen: %v", err)
			}
			return out.String()
		}
		split := screen(&chunkReader{data: []byte(input), schedule: schedule, eofEarly: eofEarly})
		if strings.Contains(split, secret) {
			t.Fatalf("planted value survived window %d overlap %d: %q", window, overlap, split)
		}
		want := m.RedactString(t.Context(), input)
		if echoes {
			want = screen(strings.NewReader(input))
		}
		if split != want {
			t.Fatalf("split changed the screen at window %d overlap %d:\n got %q\nwant %q", window, overlap, split, want)
		}
	})
}
