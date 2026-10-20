package commandprogram

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPredicatePatternsPreserveQuotedFragments(t *testing.T) {
	cases := []struct {
		kind, input string
		pattern     PredicateValue
		want        bool
	}{
		{"glob", "abc", PredicateValue{{Value: "a*"}}, true},
		{"glob", "a*", PredicateValue{{Value: "a*", Quoted: true}}, true},
		{"glob", "abc", PredicateValue{{Value: "a*", Quoted: true}}, false},
		{"glob", "a*bZZ", PredicateValue{{Value: "a*", Quoted: true}, {Value: "b*"}}, true},
		{"glob", "a/b", PredicateValue{{Value: "*"}}, true},
		{"glob", ".hidden", PredicateValue{{Value: "*"}}, true},
		{"glob", "a\nb", PredicateValue{{Value: "a?b"}}, true},
		{"ere", "prefix a.b suffix", PredicateValue{{Value: "a.b", Quoted: true}}, true},
		{"ere", "axb", PredicateValue{{Value: "a.b", Quoted: true}}, false},
		{"ere", "a.bZZ", PredicateValue{{Value: "a.", Quoted: true}, {Value: "b.*"}}, true},
		{"ere", "a\nb", PredicateValue{{Value: "^a.b$"}}, true},
		{"ere", "a\nb", PredicateValue{{Value: "^b$"}}, false},
		{"ere", "a\n", PredicateValue{{Value: "a$"}}, false},
		{"ere", "\n", PredicateValue{{Value: "^[^x]$"}}, true},
		{"ere", "abc", PredicateValue{{Value: "^[[:alpha:]]+$"}}, true},
		{"ere", "aaaa", PredicateValue{{Value: "(a{2}){2}"}}, true},
		{"ere", "aaa", PredicateValue{{Value: "a{2}|a{3}"}}, true},
		{"ere", "aaaaa", PredicateValue{{Value: "a{2}{3}"}}, false},
		{"ere", "b", PredicateValue{{Value: "a{1001}"}}, false},
		{"ere", "a", PredicateValue{{Value: "a{999999999999999999999}"}}, false},
		{"ere", "a", PredicateValue{{Value: "(a?){999999999999999999999}"}}, true},
		{"ere", "a", PredicateValue{{Value: "(^$|a){999999999999999999999}"}}, false},
		{"ere", "a", PredicateValue{{Value: "(^|a){999999999999999999999}"}}, true},
		{"ere", "(]", PredicateValue{{Value: `[\]a(]{2}`}}, true},
		{"ere", strings.Repeat("a", 1001), PredicateValue{{Value: "a{1001}"}}, true},
		{"ere", "éé", PredicateValue{{Value: "é{2}"}}, true},
		{"ere", "", PredicateValue{{Value: "a{0}"}}, true},
		{"ere", "aaaa", PredicateValue{{Value: "a{2,}"}}, true},
		{"ere", "aaa", PredicateValue{{Value: "a{2,4}"}}, true},
	}
	for _, c := range cases {
		t.Run(c.kind+" "+predicateText(c.pattern), func(t *testing.T) {
			got, err := (NativePredicatePatterns{LocalBoundsOnly: true}).MatchPredicate(t.Context(), c.kind, c.input, c.pattern)
			testutil.FailErr(t, "match native pattern", err)
			if got != c.want {
				t.Fatalf("matched %v want %v", got, c.want)
			}
		})
	}
}
func TestPredicatePatternsRejectSyntaxAndStopOnResources(t *testing.T) {
	for _, pattern := range []string{"[", "(", "(?=a)", `(a)\1`} {
		_, err := (NativePredicatePatterns{LocalBoundsOnly: true}).MatchPredicate(t.Context(), "ere", "a", PredicateValue{{Value: pattern}})
		if !errors.Is(err, errPredicateUsage) {
			t.Fatalf("ERE %q accepted/incorrect refusal: %v", pattern, err)
		}
	}
	for _, pattern := range []string{"(a+)+$"} {
		_, err := (NativePredicatePatterns{LocalBoundsOnly: true}).MatchPredicate(t.Context(), "ere", strings.Repeat("a", 100)+"!", PredicateValue{{Value: pattern}})
		var limit *PredicateLimitError
		if !errors.As(err, &limit) {
			t.Fatalf("valid large count must be resource refusal: %q %v", pattern, err)
		}
	}
	_, err := (NativePredicatePatterns{LocalBoundsOnly: true}).MatchPredicate(t.Context(), "ere", "a", PredicateValue{{Value: strings.Repeat("a", 513)}})
	var limit *PredicateLimitError
	if !errors.As(err, &limit) || limit.Resource != "regex_pattern_bytes" {
		t.Fatalf("pattern limit: %v", err)
	}
}
