package history

import (
	"github.com/lycaon/lycaon/pkg/api"
)

// requestProjection separates canonical history from the live request frame.
type requestProjection struct {
	messages []api.Message
	history  []api.Message
	frame    []api.Message
	ids      map[string]bool
}

func projectRequest(messages, history []api.Message) requestProjection {
	p := requestProjection{messages: messages, ids: make(map[string]bool, len(history))}
	for _, row := range history {
		if row.ID != "" {
			p.ids[row.ID] = true
		}
	}
	for _, row := range messages {
		if p.ids[row.ID] {
			p.history = append(p.history, row)
		} else {
			p.frame = append(p.frame, row)
		}
	}
	return p
}

// replace keeps frame rows in their original order and restores fitted history by identity.
func (p requestProjection) replace(history []api.Message) []api.Message {
	out := make([]api.Message, 0, len(p.frame)+len(history))
	next := 0
	for _, row := range p.messages {
		if !p.ids[row.ID] {
			out = append(out, row)
			continue
		}
		for next < len(history) && !p.ids[history[next].ID] {
			out = append(out, history[next])
			next++
		}
		if next < len(history) && history[next].ID == row.ID {
			out = append(out, history[next])
			next++
		}
		for next < len(history) && !p.ids[history[next].ID] {
			out = append(out, history[next])
			next++
		}
	}
	return append(out, history[next:]...)
}
