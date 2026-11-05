package observability

import (
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// LogLLMPreparation captures disposable context without recording a session task.
func LogLLMPreparation(providerID, model, sessionID string, messages []api.Message, tools []LLMToolCapture, elapsed time.Duration, errMsg string) {
	log := activeLLMCaptureLog()
	if log == nil {
		return
	}
	names, schemas := redactToolCaptures(tools)
	log.write(llmCaptureEntry{
		Time: time.Now().UTC(), ProviderID: providerID, Model: model, Call: "prepare",
		SessionID: sessionID, DurationMs: elapsed.Milliseconds(), Error: RedactCaptureText(errMsg),
		Messages: dedupeMessageBodies(RedactMessagesForCapture(messages)), ToolNames: names, Tools: schemas,
	})
}
