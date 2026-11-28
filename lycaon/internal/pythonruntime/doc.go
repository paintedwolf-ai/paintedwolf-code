// Package pythonruntime defines the Go-hosted Portable Python runtime contract:
// immutable image manifest verification, Wasm binary import auditing, fresh
// wazero guest lifecycle management, the byte-oriented workspace filesystem
// adapter boundary (implemented by PY03), and reached-operation admission with
// atomic terminal stops (implemented by PY04).
package pythonruntime
