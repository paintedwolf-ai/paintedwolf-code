package main

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestResourceGrowthUsesSteadyStateWindows(t *testing.T) {
	sampler := &processResourceSampler{baseline: 2, samples: []processResourceSample{
		{rssBytes: 1, fds: 1},
		{rssBytes: 100, fds: 20},
		{rssBytes: 110, fds: 21},
		{rssBytes: 120, fds: 22},
		{rssBytes: 130, fds: 23},
		{rssBytes: 140, fds: 24},
	}}
	result := summarizeResourceSamples(sampler.samples, sampler.baseline)
	if result.RSSGrowthBytes != 20 || result.FDGrowth != 2 {
		t.Fatalf("steady growth = RSS %d FDs %d, want 20 and 2", result.RSSGrowthBytes, result.FDGrowth)
	}
	if result.PeakRSSBytes != 140 || result.PeakFDs != 24 {
		t.Fatalf("peaks = RSS %d FDs %d", result.PeakRSSBytes, result.PeakFDs)
	}
}

func TestIsDecimalFD(t *testing.T) {
	for _, value := range []string{"0", "42", "999"} {
		if !isDecimal(value) {
			t.Errorf("isDecimal(%q) = false", value)
		}
	}
	for _, value := range []string{"", "txt", "12u"} {
		if isDecimal(value) {
			t.Errorf("isDecimal(%q) = true", value)
		}
	}
}

func TestMergeResourceSummariesUsesFinalGrowthAndLifetimePeaks(t *testing.T) {
	got := mergeResourceSummaries([]resourceSummary{
		{Samples: 5, PeakRSSBytes: 500, RSSGrowthBytes: 100, PeakFDs: 20, FDGrowth: 8},
		{Samples: 7, PeakRSSBytes: 400, RSSGrowthBytes: 10, PeakFDs: 30, FDGrowth: 2},
	})
	if got.Samples != 12 || got.PeakRSSBytes != 500 || got.PeakFDs != 30 {
		t.Fatalf("merged lifetime resources = %+v", got)
	}
	if got.RSSGrowthBytes != 10 || got.FDGrowth != 2 {
		t.Fatalf("merged growth = RSS %d FDs %d, want final segment", got.RSSGrowthBytes, got.FDGrowth)
	}
}

func TestUint64DeltaSaturatesWithoutWrapping(t *testing.T) {
	const maxSigned = int64(1<<63 - 1)
	for _, test := range []struct {
		name              string
		current, baseline uint64
		want              int64
	}{
		{name: "growth", current: 12, baseline: 5, want: 7},
		{name: "shrink", current: 5, baseline: 12, want: -7},
		{name: "positive saturation", current: ^uint64(0), want: maxSigned},
		{name: "negative saturation", baseline: ^uint64(0), want: -maxSigned},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := uint64Delta(test.current, test.baseline); got != test.want {
				t.Fatalf("uint64Delta(%d, %d) = %d, want %d", test.current, test.baseline, got, test.want)
			}
		})
	}
}

func TestRSSMeasurementRejectsMissingAndInvalidValues(t *testing.T) {
	for _, value := range []string{"", "unavailable", "0", "-1", "9223372036854775807", "10 20"} {
		t.Run(value, func(t *testing.T) {
			if got, err := parseRSSBytes(value); err == nil || got != 0 {
				t.Fatalf("parseRSSBytes(%q) = %d, %v; want invalid measurement", value, got, err)
			}
		})
	}
	if got, err := parseRSSBytes(" 42\n"); err != nil || got != 42*1024 {
		t.Fatalf("valid RSS = %d, %v", got, err)
	}
}

func TestResourceSamplerPreservesMeasurementFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sampler := startProcessResourceSampler(ctx, os.Getpid())
	summary, err := sampler.finish()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sampling error = %v; want cancellation", err)
	}
	if summary.Samples != 0 {
		t.Fatalf("failed measurements counted as %d samples", summary.Samples)
	}
}

func TestFDMeasurementRequiresNumericDescriptors(t *testing.T) {
	for _, output := range []string{"", "p123\nn/tmp/file\n", "p123\nfcwd\nn/tmp\n", "unavailable"} {
		if count, err := parseFDCount(output); err == nil || count != 0 {
			t.Fatalf("parseFDCount(%q) = %d, %v; want missing measurement", output, count, err)
		}
	}
	if count, err := parseFDCount("p123\nfcwd\nf0\nn/dev/null\nf42\nn/tmp/file\n"); err != nil || count != 2 {
		t.Fatalf("numeric descriptor count = %d, %v", count, err)
	}
}
