package secretmatch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// The screen runs ahead of every model request over every message in it, and a
// session re-sends most of its context each turn. These measure both shapes.

// transcriptMessage is one screened field: the caller's label and the body.
type transcriptMessage struct {
	label string
	text  string
}

// syntheticTranscript builds a coordinator-shaped context — source, JSON tool
// results, prose — with no credential in it. The no-hit path is the one almost
// every request pays in full.
func syntheticTranscript(approxBytes int) []transcriptMessage {
	var out []transcriptMessage
	total := 0
	for i := 0; total < approxBytes; i++ {
		var body string
		switch i % 3 {
		case 0:
			body = goSourceBlock(i)
		case 1:
			body = jsonToolResult(i)
		default:
			body = proseBlock(i)
		}
		out = append(out, transcriptMessage{label: "", text: body})
		total += len(body)
	}
	return out
}

func goSourceBlock(seed int) string {
	var b strings.Builder
	for i := range 120 {
		fmt.Fprintf(&b, "%d\tfunc handler%d%d(ctx context.Context, req *Request) (*Response, error) {\n",
			i+1, seed, i)
		fmt.Fprintf(&b, "%d\t\tif err := req.Validate(); err != nil { return nil, fmt.Errorf(\"validate: %%w\", err) }\n", i+2)
	}
	return b.String()
}

func jsonToolResult(seed int) string {
	var b strings.Builder
	b.WriteString(`{"receipt":{"tool":"read","paths_touched":1},"results":[`)
	for i := range 90 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"path":"src/pkg%d/module_%d.go","size":%d,"type":"file"}`, seed, i, 1200+i)
	}
	b.WriteString(`]}`)
	return b.String()
}

func proseBlock(seed int) string {
	var b strings.Builder
	for i := range 60 {
		b.WriteString("The coordinator surveys the workspace and reports what it observed in run ")
		b.WriteString(strconv.Itoa(seed*100 + i))
		b.WriteString(", citing paths and line numbers rather than recollection.\n")
	}
	return b.String()
}

func screenTranscript(b *testing.B, m *Matcher, msgs []transcriptMessage) {
	b.Helper()
	ctx := context.Background()
	for _, msg := range msgs {
		_ = m.ScreenLabeledContext(ctx, msg.label, msg.text)
	}
}

func benchMatcher(b *testing.B) *Matcher {
	b.Helper()
	m, err := BuildMatcher(Bundled())
	if err != nil {
		b.Fatal(err)
	}
	return m
}

// BenchmarkScreenRequestContextCold resets the cache without rebuilding the detector.
func BenchmarkScreenRequestContextCold(b *testing.B) {
	for _, size := range []int{64 << 10, 180 << 10, 450 << 10} {
		b.Run(strconv.Itoa(size>>10)+"KB", func(b *testing.B) {
			msgs := syntheticTranscript(size)
			m := benchMatcher(b)
			b.SetBytes(int64(size))
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				m.screenCache = newScreenCache()
				b.StartTimer()
				screenTranscript(b, m, msgs)
			}
		})
	}
}

// BenchmarkScreenRequestContextWarm measures repeated screening with a populated cache.
func BenchmarkScreenRequestContextWarm(b *testing.B) {
	for _, size := range []int{64 << 10, 180 << 10, 450 << 10} {
		b.Run(strconv.Itoa(size>>10)+"KB", func(b *testing.B) {
			msgs := syntheticTranscript(size)
			m := benchMatcher(b)
			screenTranscript(b, m, msgs)
			b.SetBytes(int64(size))
			b.ResetTimer()
			for range b.N {
				screenTranscript(b, m, msgs)
			}
		})
	}
}

// BenchmarkScreenGrowingSession re-screens a transcript that grows by one
// message per turn. Reported time is the whole session, not one turn.
func BenchmarkScreenGrowingSession(b *testing.B) {
	const turns = 24
	msgs := syntheticTranscript(180 << 10)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		m := benchMatcher(b)
		b.StartTimer()
		for turn := 1; turn <= turns && turn <= len(msgs); turn++ {
			screenTranscript(b, m, msgs[:turn])
		}
	}
}
