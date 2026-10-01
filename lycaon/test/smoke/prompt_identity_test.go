package smoke_test

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestSmokePromptStoredCorrelatesAdmittedMessage(t *testing.T) {
	const admittedID = "5ae0fd7b-e8bc-4d20-a86e-772c14c607c5"
	page := api.SessionTranscriptPage{Messages: []api.Message{
		{ID: "e4d3daec-d9bf-482a-8196-7230508f0db4", Role: "user", Content: "hello"},
	}}
	if smokePromptStored(page.Messages, admittedID, "hello") {
		t.Fatal("another same-text prompt established admission")
	}
	page.Messages = append(page.Messages, api.Message{
		ID: admittedID, Role: "user", Content: "hello", CreatedAt: time.Unix(1, 0),
	})
	if !smokePromptStored(page.Messages, admittedID, "hello") {
		t.Fatal("admitted message was not recognized independently of observation time")
	}
}
