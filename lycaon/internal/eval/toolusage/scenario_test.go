package toolusage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestScenarioCompactsBetweenCompletedPrompts(t *testing.T) {
	for _, outcome := range []string{"reduced", "no-op", "error"} {
		t.Run(outcome, func(t *testing.T) {
			var actions []string
			var prompt string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("missing fixture authorization")
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/compact"):
					actions = append(actions, "compact")
					if r.Method != http.MethodPost {
						t.Error("compaction must be POST")
					}
					if outcome == "error" {
						w.WriteHeader(http.StatusConflict)
						return
					}
					after := 100
					if outcome == "no-op" {
						after = 1000
					}
					_ = json.NewEncoder(w).Encode(wire.SessionCompactResponse{Generation: 1, TokensBefore: 1000, TokensAfter: after})
				case strings.HasSuffix(r.URL.Path, "/prompts"):
					var req wire.PromptRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Errorf("decode prompt: %v", err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					prompt = req.Text
					actions = append(actions, prompt)
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(wire.PromptAcceptedResponse{Status: "queued", OperationID: "submission", MessageID: "submission"})
				case strings.HasSuffix(r.URL.Path, "/messages"):
					page := wire.SessionTranscriptPage{Messages: []wire.Message{{ID: "submission", Role: wire.MessageRoleUser, Content: prompt, CreatedAt: time.Now()}, {Role: wire.MessageRoleAssistant, Content: "Completed."}}}
					if r.URL.Query().Get("after") == "0" {
						page = wire.SessionTranscriptPage{AfterCursor: "1"}
					}
					_ = json.NewEncoder(w).Encode(page)
				default:
					_ = json.NewEncoder(w).Encode(wire.Session{ID: "session", Status: wire.SessionStatusIdle})
				}
			}))
			defer server.Close()
			client := liveClient{base: server.URL, token: "fixture", http: server.Client()}
			err := client.runScenario(t.Context(), "session", CorpusTask{Prompt: "observe", FollowUps: []CorpusFollowUp{
				{Prompt: "remember", CompactBefore: true}, {Prompt: "continue"},
			}}, time.Second)
			want := []string{"observe", "compact"}
			if outcome == "reduced" {
				testutil.FailErr(t, "run scenario", err)
				want = append(want, "remember", "continue")
			} else if err == nil || !strings.Contains(err.Error(), "follow-up 1 compaction") {
				t.Fatalf("invalid compaction must stop follow-up: %v", err)
			}
			if !reflect.DeepEqual(actions, want) {
				t.Fatalf("actions=%v, want %v", actions, want)
			}
		})
	}
}
