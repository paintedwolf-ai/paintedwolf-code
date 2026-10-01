package tools

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/secretharvest"
)

// CredentialFiles receives credential-file text a read delivered and values a
// write took from the model's own arguments.
type CredentialFiles interface {
	// Delivered records the session's exposure and admits the read's bindings
	// to the session tree's evidence.
	Delivered(ctx context.Context, read CredentialFileRead) error
	// Authored records values the model already holds.
	Authored(ctx context.Context, written AuthoredCredentialValues) error
}

// CredentialFileRead is the text one read delivered from a credential file.
// RootID and Path are empty for a file outside the attached roots.
type CredentialFileRead struct {
	ProjectID, SessionID, RootSessionID string
	RootID, Path                        string
	Container                           string
	Content                             string
}

// AuthoredCredentialValues are the bindings of one credential-file write whose
// bytes appear literally in the call's reference-bearing arguments.
type AuthoredCredentialValues struct {
	ProjectID, RootSessionID, SessionID, ToolCallID string
	RootID, Path                                    string
	Values                                          []string
}

// ObserveCredentialRead reports a credential-file read. displayPath names the
// file as the read addressed it.
func (tc ToolContext) ObserveCredentialRead(ctx context.Context, absPath, displayPath, content string) error {
	if tc.CredentialFiles == nil {
		return nil
	}
	read := CredentialFileRead{
		ProjectID: tc.ProjectID, SessionID: tc.SessionID, RootSessionID: tc.ChatSessionID(),
		Container: displayPath, Content: content,
	}
	classified := absPath
	if root, path, ok := tc.SourceLocation(absPath); ok {
		read.RootID, read.Path, classified = root.ID, path, path
	}
	if !protectedpath.IsCredentialFile(classified) {
		return nil
	}
	return tc.CredentialFiles.Delivered(ctx, read)
}

// RecordModelAuthoredCredentials records the bindings of a landed credential
// file that appear literally in this call's arguments. A resolved reference
// never qualifies: the arguments hold its token.
func (tc ToolContext) RecordModelAuthoredCredentials(ctx context.Context, absPath string, content []byte) {
	if tc.CredentialFiles == nil || tc.ProjectID == "" || len(content) == 0 {
		return
	}
	root, path, ok := tc.SourceLocation(absPath)
	if !ok || !protectedpath.IsCredentialFile(path) {
		return
	}
	emitted := argumentStrings(tc.CanonicalArgs)
	if len(emitted) == 0 {
		return
	}
	var values []string
	for _, pair := range secretharvest.ParseContainer(path, content) {
		for _, text := range emitted {
			if strings.Contains(text, pair.Value) {
				values = append(values, pair.Value)
				break
			}
		}
	}
	if len(values) == 0 {
		return
	}
	err := tc.CredentialFiles.Authored(context.WithoutCancel(ctx), AuthoredCredentialValues{
		ProjectID: tc.ProjectID, RootSessionID: tc.ChatSessionID(),
		SessionID: tc.SessionID, ToolCallID: tc.ToolCallID,
		RootID: root.ID, Path: path, Values: values,
	})
	if err != nil {
		// Unrecorded authorship leaves the values to the harvest, which asks.
		slog.WarnContext(ctx, "credential authorship not recorded",
			"component", "secret_harvest", "tool_call_id", tc.ToolCallID, "error", err)
	}
}

// argumentStrings lists every string the model supplied in its arguments.
func argumentStrings(value any) []string {
	var out []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case string:
			if typed != "" {
				out = append(out, typed)
			}
		case []string:
			for _, item := range typed {
				walk(item)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(value)
	return out
}
