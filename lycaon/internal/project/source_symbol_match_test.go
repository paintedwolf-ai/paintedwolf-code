package project

import (
	"reflect"
	"regexp"
	"testing"
)

func TestSymbolNameMatcherTiers(t *testing.T) {
	cases := []struct {
		query, name string
		tier        symbolMatchTier
		ranges      []SourceTextRange
	}{
		{"ParseConfig", "ParseConfig", symbolTierExactCase, []SourceTextRange{{0, 11}}},
		{"parseconfig", "ParseConfig", symbolTierExact, []SourceTextRange{{0, 11}}},
		{"PARSECONFIG", "ParseConfig", symbolTierExact, []SourceTextRange{{0, 11}}},
		{"parse", "ParseConfig", symbolTierPrefix, []SourceTextRange{{0, 5}}},
		{"config", "ParseConfig", symbolTierWordStart, []SourceTextRange{{5, 11}}},
		{"config", "parse_config", symbolTierWordStart, []SourceTextRange{{6, 12}}},
		{"server", "HTTPServer", symbolTierWordStart, []SourceTextRange{{4, 10}}},
		{"encode", "Base64Encode", symbolTierWordStart, []SourceTextRange{{6, 12}}},
		{"pc", "ParseConfig", symbolTierHump, []SourceTextRange{{0, 1}, {5, 6}}},
		{"ParseCfg", "ParseConfig", symbolTierHump, []SourceTextRange{{0, 6}, {8, 9}, {10, 11}}},
		{"hs", "HTTPServer", symbolTierHump, []SourceTextRange{{0, 1}, {4, 5}}},
		{"b64e", "Base64Encode", symbolTierHump, []SourceTextRange{{0, 1}, {4, 7}}},
		{"gpc", "getParseConfig", symbolTierHump, []SourceTextRange{{0, 1}, {3, 4}, {8, 9}}},
		{"arse", "ParseConfig", symbolTierSubstring, []SourceTextRange{{1, 5}}},
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
	if tier != symbolTierWordStart || !reflect.DeepEqual(ranges, []SourceTextRange{{3, 8}}) {
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
