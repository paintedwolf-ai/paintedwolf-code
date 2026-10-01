// Package oar implements the Open Agent Rules engine: rule loading, typed
// GuardContext facts, engine-managed counters, condition evaluation, and the
// GuardPipeline runtime (Decision → reject/nudge/banner + on_fire + conformance).
//
// Production enforcement is Emit → Binding → EvaluateBlock on catalog Anchors
// (EnableAnchor).
//
// Normative spec: Open Agent Rules 1.0 — https://openagentrules.org/spec/1.0/
// (vendored artifacts under schemas/oar/).
// Implementation notes for this engine: docs/open-agent-rules.md.
package oar
