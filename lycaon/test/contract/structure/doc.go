// Package structure classifies internal directories and reports structural
// diagnostics for them.
//
// A declaration in declarations.yaml states what kind of thing a
// directory under lycaon/internal is:
//
//   - subsystem: one domain concern with declared entry points
//   - family: a set of interchangeable alternatives behind one contract
//   - container: an organizational boundary making no shared-concern promise
//
// Undeclared directories are containers by default. The package builds the Go
// import graph of the engine and computes per-directory evidence — external
// entry points, isolated members, and disconnected groups — for the budget
// report. Diagnostics are review evidence, not gates; declared rules (entry
// points and existing package references) are the enforced part.
package structure