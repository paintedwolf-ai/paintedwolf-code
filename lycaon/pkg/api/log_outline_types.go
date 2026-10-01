package api

// LogFormat is a detected log record-stream family.
type LogFormat string

const (
	LogFormatJSONLines     LogFormat = "json_lines"
	LogFormatLogfmt        LogFormat = "logfmt"
	LogFormatSyslogRFC5424 LogFormat = "syslog_rfc5424"
	LogFormatSyslogRFC3164 LogFormat = "syslog_rfc3164"
	LogFormatCEF           LogFormat = "cef"
	LogFormatLEEF          LogFormat = "leef"
	LogFormatCLF           LogFormat = "clf"
	LogFormatCombined      LogFormat = "combined"
)

// OutlineKind names which read outline payload is present.
type OutlineKind string

const (
	OutlineKindSymbols   OutlineKind = "symbols"
	OutlineKindLogDigest OutlineKind = "log_digest"
)
