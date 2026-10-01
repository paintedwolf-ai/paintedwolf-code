package logoutline

// LogFormat is a detected record-stream family. Empty = not a log.
type LogFormat string

const (
	FormatNone          LogFormat = ""
	FormatJSONLines     LogFormat = "json_lines"
	FormatLogfmt        LogFormat = "logfmt"
	FormatSyslogRFC5424 LogFormat = "syslog_rfc5424"
	FormatSyslogRFC3164 LogFormat = "syslog_rfc3164"
	FormatCEF           LogFormat = "cef"
	FormatLEEF          LogFormat = "leef"
	FormatCLF           LogFormat = "clf"
	FormatCombined      LogFormat = "combined"
)
