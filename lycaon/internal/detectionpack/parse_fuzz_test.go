package detectionpack

import "testing"

// FuzzParseRuleNeverPanics checks arbitrary pack bytes.
func FuzzParseRuleNeverPanics(f *testing.F) {
	seeds := []string{
		"title: t\nid: 00000000-0000-0000-0000-000000000000\n" +
			"logsource:\n  product: lycaon\n" +
			"detection:\n  sel:\n    CommandLine|contains: rm\n  condition: sel\nlevel: high\n",
		"detection:\n  condition: 1 of sel*\n",
		"detection:\n  condition: not (a and b) or all of them\n",
		"detection:\n  condition: a and\n",
		"detection:\n  condition:\n",
		"detection:\n  sel:\n    Argv|re: '('\n  condition: sel\n",
		"",
		"{",
		"\x00\x00",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		// A parse error is a correct outcome; only a panic fails this target.
		_, _ = ParseRule(data)
	})
}
