package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/debugretention"
	"github.com/lycaon/lycaon/internal/observability"
)

// scaleEngine is one engine process on a fixture store. Nothing it waits for
// has a deadline: the lane's job limit is the only backstop.
type scaleEngine struct {
	process *exec.Cmd
	exited  chan struct{}
	exitErr error
	client  *sidecarClient
	capture *captureTail
	logs    string
}

func startScaleEngine(ctx context.Context, binary, configDir, logs string) (*scaleEngine, error) {
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, err
	}
	performance, ok := debugpaths.Entry(debugpaths.KindPerformance)
	if !ok {
		return nil, errors.New("performance capture path is not registered")
	}
	port, err := freePort(ctx)
	if err != nil {
		return nil, err
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	stderr, err := debugretention.OpenFile(filepath.Join(logs, "engine.stderr.log"), debugretention.DefaultConfig().MaxFileBytes)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	capturePath := filepath.Join(logs, performance.Name)
	process := exec.CommandContext(ctx, binary, "serve", "--db", filepath.Join(configDir, "store.db")) //nolint:gosec // Explicit benchmark binary; exec performs no shell parsing.
	// An ended run interrupts the engine, which shuts down in order.
	process.Cancel = func() error { return process.Process.Signal(os.Interrupt) }
	process.Env = append(os.Environ(),
		"LYCAON_ADDR="+addr, "LYCAON_API_TOKEN="+token, "LYCAON_CONFIG_DIR="+configDir, "LYCAON_LLM_MOCK=1",
		"LYCAON_PERF_DEBUG=1", "LYCAON_PERF_DEBUG_FILE="+capturePath, "LYCAON_LOG_LEVEL=info")
	process.Stdout, process.Stderr = stderr, stderr
	if err := process.Start(); err != nil {
		_ = stderr.Close()
		return nil, err
	}
	engine := &scaleEngine{
		process: process, exited: make(chan struct{}), logs: logs,
		client:  &sidecarClient{base: "http://" + addr, token: token, http: &http.Client{}},
		capture: &captureTail{path: capturePath},
	}
	go func() {
		engine.exitErr = process.Wait()
		_ = stderr.Close()
		close(engine.exited)
	}()
	for {
		var health map[string]any
		if err := engine.client.request(ctx, http.MethodGet, "/health", nil, &health); err == nil && health["status"] == "ok" {
			return engine, nil
		}
		if err := engine.waitStep(ctx, 50*time.Millisecond); err != nil {
			_ = engine.stop()
			return nil, err
		}
	}
}

// waitStep pauses between observations and reports an engine that exited.
func (e *scaleEngine) waitStep(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.exited:
		return fmt.Errorf("engine exited: %w\n%s", e.exitErr, tailFile(filepath.Join(e.logs, "engine.stderr.log"), 30))
	case <-timer.C:
		return nil
	}
}

// stop interrupts the engine and waits for its ordered shutdown.
func (e *scaleEngine) stop() error {
	select {
	case <-e.exited:
		return e.exitErr
	default:
	}
	if err := e.process.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	<-e.exited
	if e.exitErr != nil {
		return fmt.Errorf("engine shutdown: %w\n%s", e.exitErr, tailFile(filepath.Join(e.logs, "engine.stderr.log"), 30))
	}
	return nil
}

func joinStop(err error, engine *scaleEngine) error {
	return errors.Join(err, engine.stop())
}

// captureTail reads the engine's performance capture as the engine writes it.
type captureTail struct {
	path    string
	offset  int64
	partial []byte
	pending []observability.PerformanceRecord
}

// next returns the records written since the last call.
func (c *captureTail) next() ([]observability.PerformanceRecord, error) {
	file, err := os.Open(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(c.offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(bufio.NewReader(file))
	if err != nil {
		return nil, err
	}
	c.offset += int64(len(data))
	data = append(c.partial, data...)
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		c.partial = data
		return nil, nil
	}
	c.partial = append([]byte(nil), data[end+1:]...)
	var records []observability.PerformanceRecord
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		var record observability.PerformanceRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("decode performance record: %w", err)
		}
		records = append(records, record)
	}
	return records, nil
}

// work waits for the engine's work record of operation.
func (c *captureTail) work(ctx context.Context, engine *scaleEngine, operation string) (map[string]int64, error) {
	return c.await(ctx, engine, func(record observability.PerformanceRecord) (map[string]int64, bool) {
		return record.Work, record.Kind == "work" && record.Operation == operation
	})
}

// gauges waits for a runtime sample whose gauges satisfy ready.
func (c *captureTail) gauges(ctx context.Context, engine *scaleEngine, ready func(map[string]int64) bool) (map[string]int64, error) {
	return c.await(ctx, engine, func(record observability.PerformanceRecord) (map[string]int64, bool) {
		if record.Kind != "runtime" || record.Runtime == nil {
			return nil, false
		}
		return record.Runtime.Gauges, ready(record.Runtime.Gauges)
	})
}

// await consumes records through the first match; later records stay pending.
func (c *captureTail) await(ctx context.Context, engine *scaleEngine, match func(observability.PerformanceRecord) (map[string]int64, bool)) (map[string]int64, error) {
	for {
		for i, record := range c.pending {
			if values, ok := match(record); ok {
				c.pending = c.pending[i+1:]
				return values, nil
			}
		}
		records, err := c.next()
		if err != nil {
			return nil, err
		}
		c.pending = records
		if len(records) > 0 {
			continue
		}
		if err := engine.waitStep(ctx, 200*time.Millisecond); err != nil {
			return nil, err
		}
	}
}
