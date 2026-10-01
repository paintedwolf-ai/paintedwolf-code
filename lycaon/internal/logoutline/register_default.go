package logoutline

func init() {
	RegisterParser(jsonLinesParser{})
	RegisterParser(logfmtParser{})
	RegisterParser(syslog5424Parser{})
	RegisterParser(syslog3164Parser{})
	RegisterParser(cefParser{})
	RegisterParser(leefParser{})
	RegisterParser(clfParser{})
	RegisterParser(combinedParser{})

	RegisterClassifierMatcher(FormatCEF, 0.85, 100, matchCEF)
	RegisterClassifierMatcher(FormatLEEF, 0.85, 100, matchLEEF)
	RegisterClassifierMatcher(FormatSyslogRFC5424, 0.85, 90, matchSyslog5424)
	RegisterClassifierMatcher(FormatSyslogRFC3164, 0.85, 88, matchSyslog3164)
	RegisterClassifierMatcher(FormatCombined, 0.85, 86, matchCombined)
	RegisterClassifierMatcher(FormatCLF, 0.85, 80, matchCLF)
	RegisterClassifierMatcher(FormatJSONLines, 0.85, 70, matchJSONLines)
	RegisterClassifierMatcher(FormatLogfmt, 0.90, 60, matchLogfmt)
}
