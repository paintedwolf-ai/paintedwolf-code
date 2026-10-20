package captureprojection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

type streamMemoryFixture struct {
	calls, releases int
	bytes           int64
	refusal         error
}

func (m *streamMemoryFixture) Reserve(_ context.Context, n int64) (func(), error) {
	m.calls++
	m.bytes = n
	if m.refusal != nil {
		return nil, m.refusal
	}
	return func() { m.releases++ }, nil
}
func streamOptionsFixture(memory *streamMemoryFixture) StreamOptions {
	return StreamOptions{WorkingMemory: memory, Bounds: secretmatch.StreamBounds{InputBytes: 64 << 20, OutputBytes: 64 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 40}}
}
func digestFixture(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

type fragmentedStream struct {
	value []byte
	step  int
}

func (r *fragmentedStream) Read(p []byte) (int, error) {
	if len(r.value) == 0 {
		return 0, io.EOF
	}
	n := min(len(p), r.step, len(r.value))
	copy(p, r.value[:n])
	r.value = r.value[n:]
	return n, nil
}

func TestScreenStreamExactWholeFieldPolicy(t *testing.T) {
	for _, step := range []int{1, 67, 65536} {
		t.Run(strconv.Itoa(step), func(t *testing.T) {
			p := testProjector(t, nil)
			token := secretmatch.ReferenceToken("12345678-1234-1234-1234-123456789abc")
			raw := append([]byte{0xff, 0x80, 0}, []byte("\r\n"+testSecret+" "+token+" "+testSecret+"\r\n")...)
			raw = append(raw, 0xfe)
			expected, err := p.Text(t.Context(), Scope{}, "capture.test", string(raw))
			if err != nil {
				t.Fatal(err)
			}
			memory := &streamMemoryFixture{}
			var out bytes.Buffer
			var spans []secretmatch.StreamSpan
			options := streamOptionsFixture(memory)
			options.Observe = func(_ context.Context, s secretmatch.StreamSpan) error { spans = append(spans, s); return nil }
			metadata, err := p.ScreenStream(t.Context(), Scope{}, "capture.test", &fragmentedStream{raw, step}, &out, options)
			if err != nil {
				t.Fatal(err)
			}
			if out.String() != expected.Value || metadata.RawBytes != int64(len(raw)) || metadata.EmittedBytes != int64(out.Len()) || metadata.RawSHA256 != digestFixture(raw) || metadata.EmittedSHA256 != digestFixture(out.Bytes()) || !metadata.Complete || !metadata.Redacted {
				t.Fatalf("wrong complete projection: metadata=%+v bytes_equal=%v", metadata, out.String() == expected.Value)
			}
			if memory.calls != 1 || memory.releases != 1 || memory.bytes != 32<<20 || len(spans) != 2 {
				t.Fatalf("lease/spans: %+v %v", memory, spans)
			}
			for _, span := range spans {
				if string(raw[span.InputStart:span.InputEnd]) != testSecret || out.String()[span.OutputStart:span.OutputEnd] != secretmatch.RedactedMarker {
					t.Fatalf("wrong marker mapping: %+v", span)
				}
			}
		})
	}
}

func TestScreenStreamEncodedLongValueAcrossCatalogBoundary(t *testing.T) {
	// A full admitted value has a nested JSON spelling 7 times longer than the
	// plain bytes. Its match begins before a catalog boundary and ends after it.
	value := strings.Repeat("\x01", secretmatch.MaxSecretBytes)
	forms := secretmatch.EncodedForms(value)
	encoded := ""
	for _, form := range forms {
		if len(form) > len(encoded) {
			encoded = form
		}
	}
	if len(encoded) <= 64<<10 {
		t.Fatal("fixture does not exceed legacy overlap")
	}
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Secret: value, RuleID: secretmatch.ManagedRuleID, NonDisclosable: true}}
	})
	p := New(m, nil)
	raw := []byte(strings.Repeat("safe\n", (1<<20)/5-20000) + " " + encoded + " " + strings.Repeat("after\n", 300000))
	expected, err := p.Text(t.Context(), Scope{}, "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	memory := &streamMemoryFixture{}
	metadata, err := p.ScreenStream(t.Context(), Scope{}, "", &fragmentedStream{raw, 32749}, &out, streamOptionsFixture(memory))
	if err != nil {
		t.Fatal(err)
	}
	if !metadata.Complete || !metadata.Redacted || out.String() != expected.Value {
		t.Fatalf("long spelling escaped or bytes changed: %+v equal=%v", metadata, out.String() == expected.Value)
	}
}

func TestScreenStreamOrdinaryOccurrenceCapIsGlobal(t *testing.T) {
	m := secretmatch.NewInertMatcher()
	value := "ordinary-fixture-value"
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Secret: value, RuleID: secretmatch.HarvestRuleID}}
	})
	p := New(m, nil)
	raw := strings.Repeat(value+" "+strings.Repeat("safe\n", 22000), 24)
	expected, err := p.Text(t.Context(), Scope{}, "", raw)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	metadata, err := p.ScreenStream(t.Context(), Scope{}, "", strings.NewReader(raw), &out, streamOptionsFixture(&streamMemoryFixture{}))
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != expected.Value || strings.Count(out.String(), secretmatch.RedactedMarker) != 16 || !metadata.Complete {
		t.Fatalf("ordinary cap changed: %+v markers=%d equal=%v", metadata, strings.Count(out.String(), secretmatch.RedactedMarker), out.String() == expected.Value)
	}
}

type streamFailWriter struct{ bytes int }

func (w *streamFailWriter) Write(p []byte) (int, error) {
	n := min(7, len(p))
	w.bytes += n
	return n, io.ErrClosedPipe
}
func TestScreenStreamTerminalControls(t *testing.T) {
	for _, mode := range []string{"admission", "input", "output", "memory", "cancel", "write", "generation", "classifications", "observer", "work"} {
		t.Run(mode, func(t *testing.T) {
			p := testProjector(t, nil)
			memory := &streamMemoryFixture{}
			options := streamOptionsFixture(memory)
			raw := testSecret + " plain data"
			ctx := t.Context()
			var out bytes.Buffer
			var dst io.Writer = &out
			var revision uint64 = 1
			calls := 0
			switch mode {
			case "admission":
				memory.refusal = io.ErrClosedPipe
			case "input":
				options.Bounds.InputBytes = 2
			case "output":
				options.Bounds.OutputBytes = 2
			case "work":
				options.Bounds.DetectorWork = 1
			case "memory":
				options.Bounds.MemoryBytes = 128
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "write":
				dst = &streamFailWriter{}
			case "generation":
				p.SetManagedSecretGeneration(func() uint64 {
					calls++
					if calls > 2 {
						revision++
					}
					return revision
				})
			case "classifications":
				p.matcher.SetIgnoredSource(func(context.Context) map[secretmatch.SecretFingerprint]bool {
					calls++
					if calls > 1 {
						return map[secretmatch.SecretFingerprint]bool{"changed": true}
					}
					return nil
				})
			case "observer":
				options.Observe = func(context.Context, secretmatch.StreamSpan) error { return io.ErrClosedPipe }
			}
			metadata, err := p.ScreenStream(ctx, Scope{}, "", strings.NewReader(raw), dst, options)
			if err == nil || metadata.Complete {
				t.Fatalf("terminal work reported complete: %+v %v", metadata, err)
			}
			if memory.refusal == nil && memory.releases != 1 {
				t.Fatalf("lease leaked: %+v", memory)
			}
			if mode == "write" && (metadata.EmittedBytes != 7 || metadata.EmittedSHA256 != digestFixture([]byte(secretmatch.RedactedMarker[:7]))) {
				t.Fatalf("partial write hash/count: %+v", metadata)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestScreenStreamInvalidUTF8LongRunProgress(t *testing.T) {
	raw := bytes.Repeat([]byte{0x80}, 3<<20)
	var out bytes.Buffer
	p := New(secretmatch.NewInertMatcher(), nil)
	metadata, err := p.ScreenStream(t.Context(), Scope{}, "", bytes.NewReader(raw), &out, streamOptionsFixture(&streamMemoryFixture{}))
	if err != nil || !metadata.Complete || !bytes.Equal(raw, out.Bytes()) {
		t.Fatalf("binary run changed or stalled: %+v %v", metadata, err)
	}
}

type revisionWriter struct {
	target *bytes.Buffer
	change func()
}

func (w revisionWriter) Write(p []byte) (int, error) {
	n, err := w.target.Write(p)
	w.change()
	return n, err
}
func TestScreenStreamRevisionChangesAfterScreenedPrefix(t *testing.T) {
	for _, mode := range []string{"generation", "classifications"} {
		t.Run(mode, func(t *testing.T) {
			p := testProjector(t, nil)
			revision := uint64(1)
			ignored := map[secretmatch.SecretFingerprint]bool{}
			p.SetManagedSecretGeneration(func() uint64 { return revision })
			p.matcher.SetIgnoredSource(func(context.Context) map[secretmatch.SecretFingerprint]bool { return ignored })
			var out bytes.Buffer
			dst := revisionWriter{target: &out, change: func() {
				if mode == "generation" {
					revision++
				} else {
					ignored = map[secretmatch.SecretFingerprint]bool{"revision": true}
				}
			}}
			raw := strings.Repeat("safe plain\n", 300000)
			metadata, err := p.ScreenStream(t.Context(), Scope{}, "", strings.NewReader(raw), dst, streamOptionsFixture(&streamMemoryFixture{}))
			var changed *secretmatch.StreamChangedError
			if !errors.As(err, &changed) || metadata.Complete || out.Len() != 65536 || metadata.EmittedBytes != int64(out.Len()) || metadata.EmittedSHA256 != digestFixture(out.Bytes()) || out.String() != raw[:out.Len()] {
				t.Fatalf("revision boundary lost exact prefix: %+v err=%v length=%d", metadata, err, out.Len())
			}
		})
	}
}

func TestScreenStreamExactInputAndOutputLimits(t *testing.T) {
	raw := []byte{0xff, 0, 'p', 'l', 'a', 'i', 'n', '\r', '\n'}
	options := streamOptionsFixture(&streamMemoryFixture{})
	options.Bounds.InputBytes = int64(len(raw))
	options.Bounds.OutputBytes = int64(len(raw))
	var out bytes.Buffer
	p := New(secretmatch.NewInertMatcher(), nil)
	metadata, err := p.ScreenStream(t.Context(), Scope{}, "", bytes.NewReader(raw), &out, options)
	if err != nil || !metadata.Complete || !bytes.Equal(out.Bytes(), raw) {
		t.Fatalf("exact limits rejected: %+v %v", metadata, err)
	}
}

func TestScreenStreamDenseEvidenceRefusesWithoutRawOutput(t *testing.T) {
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{{Secret: "abcd", RuleID: secretmatch.ManagedRuleID, NonDisclosable: true}}
	})
	p := New(m, nil)
	var out bytes.Buffer
	metadata, err := p.ScreenStream(t.Context(), Scope{}, "", strings.NewReader(strings.Repeat("abcd", 500000)), &out, streamOptionsFixture(&streamMemoryFixture{}))
	var limit *secretmatch.StreamLimitError
	if !errors.As(err, &limit) || limit.Resource != "screen_memory_bytes" || metadata.Complete || out.Len() != 0 {
		t.Fatalf("dense findings escaped memory admission: %+v %v output=%d", metadata, err, out.Len())
	}
}
