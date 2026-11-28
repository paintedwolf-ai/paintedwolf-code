package toolreach

import "testing"

func TestQuoteProgramKeepsSessionAddressesResolvable(t *testing.T) {
	for _, tc := range []struct {
		words []string
		want  string
	}{
		{[]string{"sed", "-n", "251,400p", "@output/prog.stdout"}, "sed -n 251,400p @output/prog.stdout"},
		{[]string{"@output/my file's.stdout"}, `@output/'my file'\''s.stdout'`},
		{[]string{"@Output"}, "@output"},
		{[]string{"@not-a-label/x"}, "'@not-a-label/x'"},
		{[]string{"a b"}, "'a b'"},
	} {
		if got := QuoteProgram(tc.words); got != tc.want {
			t.Errorf("QuoteProgram(%q) = %q, want %q", tc.words, got, tc.want)
		}
	}
}
