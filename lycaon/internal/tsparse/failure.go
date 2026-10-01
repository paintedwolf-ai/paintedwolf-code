package tsparse

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/odvcencio/gotreesitter"
)

// Failure records an unavailable syntax verdict, independently of source errors.
type Failure struct {
	ResourceLimit  int64  `json:"resource_limit,omitempty"`
	ResourceUnit   string `json:"resource_unit,omitempty"`
	ResourceSource string `json:"resource_source,omitempty"`
	Reason         string `json:"reason"`
	Language       string `json:"language"`
	TimeoutMS      int64  `json:"timeout_ms"`
	ElapsedMS      int64  `json:"elapsed_ms"`
	SourceBytes    int    `json:"source_bytes"`
	ParsedBytes    int    `json:"parsed_bytes"`
	Detail         string `json:"detail,omitempty"`
	Cause          error  `json:"-"`
}

func newFailure(lang *gotreesitter.Language, src []byte, reason string, timeout time.Duration, cause error) *Failure {
	name := "unknown"
	if lang != nil {
		name = lang.Name
	}
	failure := &Failure{Reason: reason, Language: name, TimeoutMS: timeout.Milliseconds(), SourceBytes: len(src), Cause: cause}
	if cause != nil {
		failure.Detail = cause.Error()
	}
	return failure
}

func (f *Failure) Error() string {
	message := fmt.Sprintf("%s parsing stopped: %s (timeout %d ms, elapsed %d ms, source %d bytes, reached %d bytes)",
		f.Language, f.Reason, f.TimeoutMS, f.ElapsedMS, f.SourceBytes, f.ParsedBytes)
	if f.ResourceLimit > 0 {
		message += fmt.Sprintf("; resource limit %d %s", f.ResourceLimit, f.ResourceUnit)
		if f.ResourceSource != "" {
			message += " (" + f.ResourceSource + ")"
		}
	}
	if f.Detail != "" {
		message += ": " + f.Detail
	} else if f.Cause != nil {
		message += ": " + f.Cause.Error()
	}
	return message
}

func (f *Failure) captureRuntime(runtime gotreesitter.ParseRuntime) {
	f.ParsedBytes = int(runtime.LastTokenEndByte)
	switch gotreesitter.ParseStopReason(f.Reason) {
	case gotreesitter.ParseStopMemoryBudget:
		f.ResourceLimit, f.ResourceUnit = runtime.MemoryBudgetBytes, "bytes"
		f.ResourceSource = runtime.MemoryBudgetStopSource
	case gotreesitter.ParseStopIterationLimit:
		f.ResourceLimit, f.ResourceUnit = int64(runtime.IterationLimit), "iterations"
	case gotreesitter.ParseStopStackDepthLimit:
		f.ResourceLimit, f.ResourceUnit = int64(runtime.StackDepthLimit), "stack frames"
	case gotreesitter.ParseStopNodeLimit:
		f.ResourceLimit, f.ResourceUnit = int64(runtime.NodeLimit), "nodes"
	default:
	}
}

func (f *Failure) Unwrap() error { return f.Cause }

func (f *Failure) Incomplete() bool {
	switch gotreesitter.ParseStopReason(f.Reason) {
	case gotreesitter.ParseStopTimeout, gotreesitter.ParseStopCancelled,
		gotreesitter.ParseStopIterationLimit, gotreesitter.ParseStopStackDepthLimit,
		gotreesitter.ParseStopNodeLimit, gotreesitter.ParseStopMemoryBudget,
		gotreesitter.ParseStopTokenSourceEOF:
		return true
	default:
		return f.Reason == "deadline" || f.Reason == "canceled"
	}
}

// Facts carries diagnostics to host feedback without embedding recovery prose.
func (f *Failure) Facts(phase string) map[string]any {
	return map[string]any{
		"parse_timed_out": f.Reason == "timeout", "parse_canceled": f.Reason == "canceled" || f.Reason == "deadline",
		"parse_reason": f.Reason, "parse_language": f.Language,
		"parse_timeout_ms": f.TimeoutMS, "parse_elapsed_ms": f.ElapsedMS,
		"parse_source_bytes": f.SourceBytes, "parse_reached_bytes": f.ParsedBytes,
		"parse_phase": phase, "parse_failure": f.Error(),
		"parse_config": "config/" + string(config.SourceParsing),
	}
}

// FileFailure associates parser diagnostics with their source snapshot.
type FileFailure struct {
	Path    string   `json:"path"`
	Phase   string   `json:"phase"`
	Failure *Failure `json:"failure"`
}

// MaxFailureExamples bounds diagnostic lists without hiding the total count.
const MaxFailureExamples = 20

// CancellationFailure represents a canceled analysis before a grammar is loaded.
// It returns nil while the request is active.
func CancellationFailure(ctx context.Context, language string, sourceBytes int) *Failure {
	if ctx.Err() == nil {
		return nil
	}
	if language == "" {
		language = "unknown"
	}
	reason, cause := contextStop(ctx)
	return &Failure{Reason: reason, Language: language, SourceBytes: sourceBytes, Detail: cause.Error(), Cause: cause}
}
