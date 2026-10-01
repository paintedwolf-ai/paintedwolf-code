// Package coordinator implements the coordinator/worker prompt runtime: tool iteration,
// LLM completion streaming, and tool policy. Session persistence and lifecycle
// stay in internal/session; session.Manager delegates Prompt to Runtime.RunPrompt.
package coordinator
