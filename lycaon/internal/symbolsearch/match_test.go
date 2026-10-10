package symbolsearch

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
)

func TestSymbolNameMatcherTiers(t *testing.T) {
	cases := []struct {
		query, name string
		tier        symbolMatchTier
		ranges      []project.SourceTextRange
	}{
		{"ParseConfig", "ParseConfig", symbolTierExactCase, []project.SourceTextRange{{0, 11}}},
		{"parseconfig", "ParseConfig", symbolTierExact, []project.SourceTextRange{{0, 11}}},
		{"PARSECONFIG", "ParseConfig", symbolTierExact, []project.SourceTextRange{{0, 11}}},
		{"parse", "ParseConfig", symbolTierPrefix, []project.SourceTextRange{{0, 5}}},
		{"config", "ParseConfig", symbolTierWordStart, []project.SourceTextRange{{5, 11}}},
		{"config", "parse_config", symbolTierWordStart, []project.SourceTextRange{{6, 12}}},
		{"server", "HTTPServer", symbolTierWordStart, []project.SourceTextRange{{4, 10}}},
		{"encode", "Base64Encode", symbolTierWordStart, []project.SourceTextRange{{6, 12}}},
		{"pc", "ParseConfig", symbolTierHump, []project.SourceTextRange{{0, 1}, {5, 6}}},
		{"ParseCfg", "ParseConfig", symbolTierHump, []project.SourceTextRange{{0, 6}, {8, 9}, {10, 11}}},
		{"hs", "HTTPServer", symbolTierHump, []project.SourceTextRange{{0, 1}, {4, 5}}},
		{"b64e", "Base64Encode", symbolTierHump, []project.SourceTextRange{{0, 1}, {4, 7}}},
		{"gpc", "getParseConfig", symbolTierHump, []project.SourceTextRange{{0, 1}, {3, 4}, {8, 9}}},
		{"arse", "ParseConfig", symbolTierSubstring, []project.SourceTextRange{{1, 5}}},
		{"pc", "package", symbolTierNone, nil},
		{"cfg", "Config", symbolTierNone, nil},
		{"pcx", "ParseConfig", symbolTierNone, nil},
		{"parseconfigs", "ParseConfig", symbolTierNone, nil},
	}
	for _, tc := range cases {
		t.Run(tc.query+"/"+tc.name, func(t *testing.T) {
			tier, ranges := newSymbolNameMatcher(tc.query).match(tc.name)
			if tier != tc.tier || !reflect.DeepEqual(ranges, tc.ranges) {
				t.Fatalf("match(%q, %q) = %d %v, want %d %v", tc.query, tc.name, tier, ranges, tc.tier, tc.ranges)
			}
		})
	}
}

func TestSymbolNameMatcherHighlightsCountCodePoints(t *testing.T) {
	tier, ranges := newSymbolNameMatcher("größe").match("MaxGröße")
	if tier != symbolTierWordStart || !reflect.DeepEqual(ranges, []project.SourceTextRange{{3, 8}}) {
		t.Fatalf("match = %d %v, want word start at code points 3..8", tier, ranges)
	}
}

// The discovery expression over-approximates word starts, so every line that
// declares a name the query abbreviates is found.
func TestSymbolHumpPatternFindsEveryAbbreviation(t *testing.T) {
	cases := []struct {
		query string
		lines []string
		miss  []string
	}{
		{"pc", []string{"func ParseConfig() {}", "def parse_config():", "x := getParseConfig()", "const PACKAGE_CACHE = 1"}, []string{"package main", "pkg.Config", "spec := 1"}},
		{"ParseCfg", []string{"func ParseConfig() {}", "let parseConfigFile = 1"}, []string{"parse(cfg)"}},
		{"b64e", []string{"func Base64Encode() {}", "base64_encode = 1"}, []string{"b64 encode"}},
		{"hs", []string{"type HTTPServer struct{}"}, []string{"hash"}},
	}
	for _, tc := range cases {
		pattern, ok := symbolHumpPattern(tc.query)
		if !ok {
			t.Fatalf("no hump pattern for %q", tc.query)
		}
		re := regexp.MustCompile(pattern)
		for _, line := range tc.lines {
			if !re.MatchString(line) {
				t.Errorf("pattern for %q misses %q", tc.query, line)
			}
		}
		for _, line := range tc.miss {
			if re.MatchString(line) {
				t.Errorf("pattern for %q matches %q", tc.query, line)
			}
		}
	}
}

func TestSymbolHumpPatternBounds(t *testing.T) {
	for _, query := range []string{"p", "parse_config", "parse config", "abcdefghijklmnopq"} {
		if _, ok := symbolHumpPattern(query); ok {
			t.Errorf("hump pattern for %q, want none", query)
		}
	}
}
