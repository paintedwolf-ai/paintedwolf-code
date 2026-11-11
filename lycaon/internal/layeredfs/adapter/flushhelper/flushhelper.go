// Package flushhelper is the child side of a mount barrier flush. An adapter re-executes the engine
// to flush its view's mount from a process that is not serving it, so a flush that waits on the
// server cannot deadlock against it.
package flushhelper

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// PauseFileEnv names a file the helper creates with its PID and waits on until a test removes it.
const PauseFileEnv = "PW_BARRIER_FLUSH_HELPER_PAUSE_FILE"

// Flush syncs each of paths through mountRoot and counts the files and directories it synced.
type Flush func(mountRoot string, paths []string) (files, dirs int64, err error)

// Request is the helper's stdin: the mount root, then each path to flush.
func Request(mountRoot string, paths []string) []byte {
	return []byte(strings.Join(append([]string{mountRoot}, paths...), "\x00"))
}

// Serve runs the helper and exits: stdout carries the counts, stderr the failure.
func Serve(flush Flush) {
	if pauseFile := os.Getenv(PauseFileEnv); pauseFile != "" {
		_ = os.WriteFile(pauseFile, fmt.Appendf(nil, "%d\n", os.Getpid()), 0o600)
		for {
			if _, err := os.Stat(pauseFile); errors.Is(err, os.ErrNotExist) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	parts := strings.Split(string(raw), "\x00")
	if parts[0] == "" {
		os.Exit(2)
	}
	paths := make([]string, 0, len(parts)-1)
	for _, p := range parts[1:] {
		if p != "" {
			paths = append(paths, p)
		}
	}
	files, dirs, err := flush(parts[0], paths)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	_, _ = fmt.Fprintf(os.Stdout, "%d %d\n", files, dirs)
	os.Exit(0)
}

// Reply reads the counts a helper wrote.
func Reply(stdout []byte) (files, dirs int64) {
	_, _ = fmt.Sscanf(strings.TrimSpace(string(stdout)), "%d %d", &files, &dirs)
	return files, dirs
}
