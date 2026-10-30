package survey

import "testing"

func TestCounterDisplayColumnsAreIndependentOfScalarLengthAndChunking(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		columns     int64
	}{
		{"tabs", "gamma\twith\ttabs\n", 20},
		{"wide", "界\t界", 10},
		{"combining", "e\u0301\tend", 11},
		{"carriage return", "long\rx", 4},
		{"form feed", "long\fx", 4},
		{"vertical tab", "a\vb", 2},
		{"backspace", "a\bb", 2},
		{"empty", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, step := range []int{1, len(tc.input) + 1} {
				var counter Counter
				for start := 0; start < len(tc.input); start += step {
					_, _ = counter.Write([]byte(tc.input[start:min(start+step, len(tc.input))]))
				}
				if got := counter.DisplayWidth(); got != tc.columns {
					t.Fatalf("step %d: columns = %d, want %d", step, got, tc.columns)
				}
				if got, want := counter.Counts(), CountBytes([]byte(tc.input)); got != want {
					t.Fatalf("step %d: scalar counts = %+v, want %+v", step, got, want)
				}
			}
		})
	}
}
