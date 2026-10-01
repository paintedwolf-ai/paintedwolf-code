package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

func TestFeedMissingCapabilityRemainsUnknown(t *testing.T) {
	doc := fixtureDoc(t)
	model := doc.Providers["openai"].Models["gpt-4.1"]
	model.ToolCall = nil
	model.Reasoning = nil
	caps := modelEntryFromFeed(model).Capabilities
	if caps.Tools.State != modelinfo.CapabilityUnknown || caps.Reasoning.State != modelinfo.CapabilityUnknown {
		t.Fatalf("missing fields became negative evidence: %+v", caps)
	}
	model.ToolCall = new(false)
	if modelEntryFromFeed(model).Capabilities.Tools.State != modelinfo.CapabilityUnsupported {
		t.Fatal("explicit negative evidence was lost")
	}
}
