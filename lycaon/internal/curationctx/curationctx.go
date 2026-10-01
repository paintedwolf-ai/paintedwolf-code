package curationctx

import (
	"context"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"strings"
)

const taskHintMaxRunes = 280

type taskHintKey struct{}
type sessionKey struct{}
type laneKey struct{}

// Session holds invocation metadata for curator debug capture and cost attribution.
type Session struct {
	SessionID       string
	OwnerPersonID   string
	Posture         string
	ProjectID       string
	Agent           string
	ParentSessionID string
	ToolCallID      string
	ProjectDir      string
}

// WithTaskHint attaches the active turn task for curator orientation.
func WithTaskHint(ctx context.Context, task string) context.Context {
	task = TruncateTaskHint(task)
	if task == "" {
		return ctx
	}
	return context.WithValue(ctx, taskHintKey{}, task)
}

// TaskHint returns a truncated task hint from ctx, if any.
func TaskHint(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(taskHintKey{}).(string)
	return strings.TrimSpace(v)
}

// WithSession attaches session identity for curator debug capture.
func WithSession(ctx context.Context, sess Session) context.Context {
	if strings.TrimSpace(sess.SessionID) == "" &&
		strings.TrimSpace(sess.OwnerPersonID) == "" &&
		strings.TrimSpace(sess.Posture) == "" &&
		strings.TrimSpace(sess.ProjectID) == "" &&
		strings.TrimSpace(sess.Agent) == "" &&
		strings.TrimSpace(sess.ParentSessionID) == "" &&
		strings.TrimSpace(sess.ToolCallID) == "" &&
		strings.TrimSpace(sess.ProjectDir) == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionKey{}, sess)
}

// SessionFrom reads session metadata from ctx.
func SessionFrom(ctx context.Context) Session {
	if ctx == nil {
		return Session{}
	}
	v, _ := ctx.Value(sessionKey{}).(Session)
	return v
}

// WithLane marks ctx as occupying the chat thinking lane.
func WithLane(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, laneKey{}, true)
}

// WithoutLane clears lane occupancy. Session identity is unchanged.
func WithoutLane(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, laneKey{}, false)
}

// LaneOccupied reports whether ctx occupies the chat thinking lane.
func LaneOccupied(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(laneKey{}).(bool)
	return v
}

// TruncateTaskHint caps a task line for curator orientation.
func TruncateTaskHint(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return runeclamp.Clamp(text, taskHintMaxRunes)
}
