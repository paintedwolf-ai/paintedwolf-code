package turnload

import (
	"strings"
	"unicode/utf8"
)

// State is the compact, typed view of a turn the decision model reads. Every
// field is a host fact or the request in its author's words; nothing is
// derived from prose.
type State struct {
	// Host is the prompt family deciding: coordinator or worker.
	Host string `json:"host"`
	// User is the visible user message, or a worker's brief, bounded.
	User string `json:"user"`
	// RootCount is the number of attached project roots.
	RootCount int `json:"root_count"`
	// Posture is the session posture (build, vet, spec, orchestrate).
	Posture string `json:"posture,omitempty"`
	// WorkersInFlight counts running worker legs.
	WorkersInFlight int `json:"workers_in_flight"`
	// Surface is the coordinator surface, or the worker's tool profile.
	Surface string `json:"surface"`
	// RecentTools names tools the chat called in earlier turns, most recent first.
	RecentTools []string `json:"recent_tools,omitempty"`
	// Attachments names the kinds of attachments on the request.
	Attachments []string `json:"attachments,omitempty"`
	// Loaded names the tools the chat requested or called.
	Loaded []string `json:"loaded,omitempty"`
}

// BoundUser truncates the request to the catalog budget, on a rune boundary.
func BoundUser(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:maxChars]))
}
