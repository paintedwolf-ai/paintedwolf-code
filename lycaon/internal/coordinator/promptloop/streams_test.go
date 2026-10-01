package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

type testMessageStreams struct {
	cachelive   func(string, string, string, []string, int)
	cachereplay func(string, string, []string)
	project     func(context.Context, string, api.Message) error
	finish      func(context.Context, string)
}

func (s *testMessageStreams) CacheLive(sessionID, messageID, content string, tokens []string, generating int) {
	if s.cachelive != nil {
		s.cachelive(sessionID, messageID, content, tokens, generating)
	}
}
func (s *testMessageStreams) CacheReplay(messageID, content string, tokens []string) {
	if s.cachereplay != nil {
		s.cachereplay(messageID, content, tokens)
	}
}
func (s *testMessageStreams) Project(ctx context.Context, sessionID string, message api.Message) error {
	if s.project != nil {
		return s.project(ctx, sessionID, message)
	}
	return nil
}
func (s *testMessageStreams) Finish(ctx context.Context, sessionID string) {
	if s.finish != nil {
		s.finish(ctx, sessionID)
	}
}
