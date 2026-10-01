//go:build !js || !wasm

package main

// main keeps the host-testable bridge package buildable outside js/wasm.
// The browser entry point lives in main.go.
func main() {}
