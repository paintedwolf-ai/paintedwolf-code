package survey

import (
	"strings"
	"testing"
)

func TestCounterSelectedMetricsAcrossChunkBoundaries(t *testing.T) {
	inputs := []string{"", "界\tone\r\nlast", "e\u0301\tend\flong\rx\n", "ok\xff\n", "a\xc3", strings.Repeat("界 word\t\n", 4096)}
	for _, input := range inputs {
		full := NewCounter(allCountMetrics)
		_, _ = full.Write([]byte(input))
		for metrics := CountMetrics(0); metrics <= allCountMetrics; metrics++ {
			want := selectedCounts(full.Counts(), metrics)
			for _, step := range []int{1, 7, len(input) + 1} {
				counter := NewCounter(metrics)
				for start := 0; start < len(input); start += step {
					_, _ = counter.Write([]byte(input[start:min(start+step, len(input))]))
				}
				if got := counter.Counts(); got != want {
					t.Fatalf("bytes %d metrics %d chunk %d: counts %+v, want %+v", len(input), metrics, step, got, want)
				}
				width := int64(0)
				if metrics&CountDisplayWidth != 0 {
					width = full.DisplayWidth()
				}
				if counter.DisplayWidth() != width {
					t.Fatalf("metrics %d chunk %d: width %d, want %d", metrics, step, counter.DisplayWidth(), width)
				}
				if metrics&(CountChars|CountLineLength|CountDisplayWidth) == 0 && (counter.line != 0 || len(counter.pending) != 0 || counter.cr || counter.counts.InvalidEncoding) {
					t.Fatalf("metrics %d performed Unicode work: %+v", metrics, counter)
				}
				if metrics&CountWords == 0 && counter.inWord {
					t.Fatalf("metrics %d performed word work", metrics)
				}
			}
		}
	}
}

func selectedCounts(full TextCounts, metrics CountMetrics) TextCounts {
	out := TextCounts{Bytes: full.Bytes}
	if metrics&CountLines != 0 {
		out.Lines = full.Lines
	}
	if metrics&CountWords != 0 {
		out.Words = full.Words
	}
	if metrics&CountChars != 0 {
		out.Chars = full.Chars
	}
	if metrics&CountLineLength != 0 {
		out.MaxLineLength = full.MaxLineLength
	}
	if metrics&(CountChars|CountLineLength|CountDisplayWidth) != 0 {
		out.InvalidEncoding = full.InvalidEncoding
	}
	return out
}

func TestStructuredWcOnlyCollectsExposedMetrics(t *testing.T) {
	for _, words := range []bool{false, true} {
		counts := countWcFile([]byte("界\tone\nlast\xc3"), words)
		want := TextCounts{Bytes: 13, Lines: 1}
		if words {
			want.Words = 3
		}
		if counts != want {
			t.Fatalf("words %v: counts %+v, want %+v", words, counts, want)
		}
	}
}

func TestByteCountModesDoNotAllocateUnicodeChunkState(t *testing.T) {
	chunk := []byte{0xc3}
	for _, metrics := range []CountMetrics{0, CountLines, CountWords, CountLines | CountWords} {
		allocs := testing.AllocsPerRun(5, func() {
			counter := NewCounter(metrics)
			for range 4096 {
				_, _ = counter.Write(chunk)
			}
			if counter.Counts().Bytes != 4096 {
				t.Fatal("incomplete byte total")
			}
		})
		if allocs != 0 {
			t.Fatalf("metrics %d: %v allocations for byte chunks", metrics, allocs)
		}
	}
}
