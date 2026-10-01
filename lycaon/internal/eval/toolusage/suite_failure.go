package toolusage

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"

	wire "github.com/lycaon/lycaon/pkg/api"
)

// ExecutionFailure records host facts separately from independently graded outcomes.
type ExecutionFailure struct {
	Kind      string `json:"kind"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

func (f *ExecutionFailure) Error() string {
	return fmt.Sprintf("%s interrupted the attempt (%s)", f.Kind, f.Code)
}

func submissionFailure(code string) *ExecutionFailure {
	if code == "" {
		code = "submission_failed"
	}
	f := &ExecutionFailure{Kind: "application", Code: code}
	switch wire.NoticeCode(code) {
	case wire.NoticeCodeProviderToolCallsInProse:
		f.Kind = "model"
	case wire.NoticeCodeProviderOverloaded, wire.NoticeCodeProviderRateLimited,
		wire.NoticeCodeProviderServerError, wire.NoticeCodeProviderSilent, wire.NoticeCodeProviderUnreachable,
		wire.NoticeCodeProviderEmptyCompletion:
		f.Kind, f.Retryable = "provider", true
	case wire.NoticeCodeProviderRequestRejected, wire.NoticeCodeModelRefused,
		wire.NoticeCodeProviderNotConfigured, wire.NoticeCodeProviderContextTooSmall,
		wire.NoticeCodeProviderResponseInterrupted:
		f.Kind = "provider"
	default:
	}
	return f
}

func openFailureObserver(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
