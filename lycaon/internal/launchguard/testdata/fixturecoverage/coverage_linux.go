//go:build linux

// Package fixturecoverage owns counter collection for instrumented native launch
// fixtures. It is imported only by their overlay and excluded from its coverpkg.
package fixturecoverage

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime/coverage"

	"golang.org/x/sys/unix"
)

// Collector retains the fixture-owned directory across the real Env scrub.
type Collector struct {
	directory  string
	ready, ack byte
}

// New runs in launchguard initialization, before main's coverage initHook.
func New(encodedDirectory string, ready, ack byte) *Collector {
	raw, err := hex.DecodeString(encodedDirectory)
	if err != nil || !filepath.IsAbs(string(raw)) {
		panic("invalid embedded native fixture collector")
	}
	if err := os.Setenv("GOCOVERDIR", string(raw)); err != nil {
		panic(err)
	}
	return &Collector{directory: string(raw), ready: ready, ack: ack}
}

func (collector *Collector) flush() error {
	if err := coverage.WriteMetaDir(collector.directory); err != nil {
		return fmt.Errorf("fixture stage metadata: %w", err)
	}
	if err := coverage.WriteCountersDir(collector.directory); err != nil {
		return fmt.Errorf("fixture stage counters: %w", err)
	}
	return nil
}

// Exec preserves the actual exec boundary and its empty environment contract.
func (collector *Collector) Exec(execute func(string, []string, []string) error, path string, args, environment []string) error {
	if err := collector.flush(); err != nil {
		return err
	}
	return execute(path, args, environment)
}

// Send snapshots actual owner work before its typed progress message, never
// final counters from workers the guardian later kills.
func (collector *Collector) Send(send func(int, []byte, []byte, unix.Sockaddr, int) (int, error), socket int, packet, control []byte, address unix.Sockaddr, flags int, kind byte) (int, error) {
	if kind == collector.ready || kind == collector.ack {
		if err := collector.flush(); err != nil {
			return 0, err
		}
	}
	return send(socket, packet, control, address, flags)
}
