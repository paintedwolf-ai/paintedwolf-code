package secretmint

import "testing"

func TestMeasuresMaterialWithoutSelectingAWeakness(t *testing.T) {
	ins := testInspector(t)
	for _, tc := range []struct {
		value                       string
		length, words, run, classes int
		listed                      bool
	}{
		{"password", 8, 1, 8, 1, true},
		{"correct-horse-battery-staple", 28, 4, 28, 1, false},
		{"secret-secret-secret-secret", 27, 1, 27, 1, false},
		{"k7Rm2Qx9Wz4Pv8Ns", 16, 1, 16, 3, false},
		{"雪雪雪", 3, 1, 3, 0, false},
	} {
		got := ins.measure(tc.value)
		want := Measurements{tc.listed, tc.length, tc.words, tc.run, tc.classes}
		if got != want {
			t.Errorf("measure(%q) = %#v, want %#v", tc.value, got, want)
		}
	}
}

func TestLiteralCandidatesExcludeReferences(t *testing.T) {
	ins := testInspector(t)
	for _, value := range []string{"$PASSWORD", "${PASSWORD}", "$(generate)", "`generate`", ""} {
		if got := ins.candidate("password", value); len(got) != 0 {
			t.Errorf("reference %q produced a candidate", value)
		}
	}
}
