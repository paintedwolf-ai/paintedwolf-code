package main

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"io"
)

// Format readers use ReadAt, preserving the offset used to hash the artifact.
func executableFormat(source io.ReaderAt) string {
	if _, err := elf.NewFile(source); err == nil {
		return "elf"
	}
	if _, err := macho.NewFile(source); err == nil {
		return "macho"
	}
	if _, err := macho.NewFatFile(source); err == nil {
		return "macho-fat"
	}
	if _, err := pe.NewFile(source); err == nil {
		return "pe"
	}
	return "launcher"
}
