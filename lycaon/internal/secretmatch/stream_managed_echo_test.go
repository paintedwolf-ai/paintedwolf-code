package secretmatch

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"
)

func screenManagedEcho(t *testing.T, values []HarvestedValue, input io.Reader, policy StreamPolicy) string {
	t.Helper()
	m := NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []HarvestedValue { return values })
	var output bytes.Buffer
	_, err := m.ScreenBytes(t.Context(), input, &output, "", policy, StreamBounds{
		InputBytes: 8 << 20, OutputBytes: 8 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 36,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("screen managed echo: %v", err)
	}
	return output.String()
}

func TestStreamManagedEchoWholeValuesUseOnlyCurrentReferences(t *testing.T) {
	const secret = "orchard-protected-value-7731"
	for _, retired := range []bool{false, true} {
		value := managedValue(secret, testReference)
		value.Retired = retired
		if retired {
			value.Reference = ""
		}
		for _, input := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret))} {
			for split := 1; split < len(input); split++ {
				out := screenManagedEcho(t, []HarvestedValue{value}, io.MultiReader(strings.NewReader(input[:split]), strings.NewReader(input[split:])), StreamPolicy{ManagedEchoes: true})
				if strings.Contains(out, secret) || strings.Contains(out, input) {
					t.Fatalf("retired=%v split=%d: secret survived", retired, split)
				}
				if strings.Contains(out, testReference) == retired {
					t.Fatalf("retired=%v split=%d: wrong reference standing: %q", retired, split, out)
				}
			}
		}
	}
}

func TestStreamManagedEchoEmbeddedBase64MasksAcrossChunkBoundaries(t *testing.T) {
	const secret = "orchard-protected-value-7731\xfb\xff"
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	for index, encoding := range encodings {
		for prefix := 0; prefix < 3; prefix++ {
			t.Run(fmt.Sprintf("encoding%d/prefix%d", index, prefix), func(t *testing.T) {
				encoded := encoding.EncodeToString([]byte(strings.Repeat("!", prefix) + secret + "?suffix"))
				input := "before: " + encoded + " :after"
				for split := 1; split < len(input); split++ {
					out := screenManagedEcho(t, []HarvestedValue{managedValue(secret, testReference)}, io.MultiReader(strings.NewReader(input[:split]), strings.NewReader(input[split:])), StreamPolicy{ManagedEchoes: true})
					if out == input || strings.Contains(out, secret) || strings.Contains(out, encoded) || strings.Contains(out, testReference) {
						t.Fatalf("split=%d: embedded value was not masked without reference: %q", split, out)
					}
					if !strings.HasPrefix(out, "before: ") || !strings.HasSuffix(out, " :after") {
						t.Fatalf("split=%d: surrounding bytes changed: %q", split, out)
					}
				}
			})
		}
	}
}

func TestStreamManagedEchoPolicyLeavesDefaultAndUnknownBytesUnchanged(t *testing.T) {
	const secret = "orchard-protected-value-7731"
	value := managedValue(secret, testReference)
	m := NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []HarvestedValue { return []HarvestedValue{value} })
	for _, input := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), base64.StdEncoding.EncodeToString([]byte(secret + "?suffix"))} {
		out := screenManagedEcho(t, []HarvestedValue{value}, strings.NewReader(input), StreamPolicy{})
		if want := m.RedactString(t.Context(), input); out != want {
			t.Fatalf("default policy changed: got %q want %q", out, want)
		}
	}
	unknown := base64.StdEncoding.EncodeToString([]byte("ordinary bytes?not a managed value"))
	if out := screenManagedEcho(t, []HarvestedValue{value}, strings.NewReader(unknown), StreamPolicy{ManagedEchoes: true}); out != unknown {
		t.Fatalf("unknown bytes changed: %q", out)
	}
}

func TestStreamManagedEchoCrossesPublicationWindows(t *testing.T) {
	const secret = "orchard-protected-value-7731"
	encoded := base64.RawStdEncoding.EncodeToString([]byte("!" + secret + "?suffix"))
	boundary := MaxScreenWindowBytes - screenWindowOverlapBytes
	for offset := -len(encoded); offset <= len(encoded); offset += 7 {
		prefix := strings.Repeat(".", boundary+offset)
		input := prefix + encoded + strings.Repeat(".", MaxScreenWindowBytes)
		out := screenManagedEcho(t, []HarvestedValue{managedValue(secret, testReference)}, strings.NewReader(input), StreamPolicy{ManagedEchoes: true})
		if strings.Contains(out, encoded) || strings.Contains(out, secret) || strings.Contains(out, testReference) || out == input {
			t.Fatalf("offset=%d: publication boundary exposed or referenced embedded bytes", offset)
		}
		if !strings.HasPrefix(out, prefix) || !strings.HasSuffix(out, strings.Repeat(".", MaxScreenWindowBytes)) {
			t.Fatalf("offset=%d: screening changed unrelated bytes", offset)
		}
	}
}

func TestStreamManagedEchoWorkLimitDoesNotPublishUnscreenedBytes(t *testing.T) {
	const secret = "orchard-protected-value-7731"
	m := NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []HarvestedValue {
		return []HarvestedValue{managedValue(secret, testReference)}
	})
	input := base64.StdEncoding.EncodeToString([]byte(secret + "?suffix"))
	var output bytes.Buffer
	_, err := m.ScreenBytes(t.Context(), strings.NewReader(input), &output, "", StreamPolicy{ManagedEchoes: true}, StreamBounds{
		InputBytes: 1024, OutputBytes: 1024, MemoryBytes: 32 << 20, DetectorWork: 1,
	}, nil, nil, nil)
	if err == nil || output.Len() != 0 {
		t.Fatalf("work limit published unverified bytes: err=%v output=%q", err, output.String())
	}
}

func TestStreamManagedEchoWithoutEvidenceDoesNotDecode(t *testing.T) {
	m := NewInertMatcher()
	raw := strings.Repeat("QUJD", 1000)
	var output bytes.Buffer
	_, err := m.ScreenBytes(t.Context(), strings.NewReader(raw), &output, "", StreamPolicy{ManagedEchoes: true}, StreamBounds{InputBytes: 8192, OutputBytes: 8192, MemoryBytes: 32 << 20, DetectorWork: 1}, nil, nil, nil)
	if err != nil || output.String() != raw {
		t.Fatalf("empty evidence changed capture: err=%v output=%d", err, output.Len())
	}
}

func TestStreamManagedEchoScanRespectsWorkLimit(t *testing.T) {
	m := NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []HarvestedValue {
		return []HarvestedValue{managedValue("known-protected-value", testReference)}
	})
	var output bytes.Buffer
	_, err := m.ScreenBytes(t.Context(), strings.NewReader(strings.Repeat(".", 4096)), &output, "", StreamPolicy{ManagedEchoes: true}, StreamBounds{InputBytes: 8192, OutputBytes: 8192, MemoryBytes: 32 << 20, DetectorWork: 100}, nil, nil, nil)
	if err == nil || output.Len() != 0 {
		t.Fatalf("linear scan escaped work bound: err=%v output=%d", err, output.Len())
	}
}
