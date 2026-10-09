// Package scanworker defines the private subprocess boundary for library scanners.
package scanworker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/drivers/library"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// Request is one scanner invocation carried over stdin.
type Request struct {
	FingerprintKey []byte `json:"fingerprint_key,omitempty"`
	// AdvisoryDatabase is a provisioned OSV export; empty refreshes the host cache.
	AdvisoryDatabase string           `json:"advisory_database,omitempty"`
	Impl             string           `json:"impl"`
	ID               string           `json:"id"`
	Jobs             int              `json:"jobs"`
	Scan             scan.ScanRequest `json:"scan"`
}

// Response is the only stdout shape emitted by a worker.
type Response struct {
	SecretIdentities []scanoutput.SecretIdentity `json:"secret_identities,omitempty"`
	Result           *scanoutput.Result          `json:"result,omitempty"`
	Error            string                      `json:"error,omitempty"`
}

// IdleExit bounds how long an unused worker keeps its engines in memory.
const IdleExit = 5 * time.Minute

// Serve emits one response per request and reuses engines until the worker exits.
func Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	lines := bufio.NewReaderSize(input, 64<<10)
	engines := make(map[string]scan.CodeScanner)
	for {
		line, err := readRequestLine(ctx, lines)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var req Request
		if err := decoder.Decode(&req); err != nil {
			return fmt.Errorf("decode scan worker request: %w", err)
		}
		key := string(req.FingerprintKey) + "\x00" + strings.TrimSpace(req.Impl) + "\x00" + strings.TrimSpace(req.ID) + "\x00" + strconv.Itoa(req.Jobs)
		worker, ok := engines[key]
		if !ok {
			worker, err = scanner(req)
			if err != nil {
				if err := writeResponse(output, Response{Error: err.Error()}); err != nil {
					return err
				}
				continue
			}
			engines[key] = worker
		}
		req.Scan.ScannerID = strings.TrimSpace(req.ID)
		result, runErr := worker.Run(ctx, req.Scan)
		if err := writeResponse(output, workerResponse(result, runErr)); err != nil {
			return err
		}
	}
}

// readRequestLine reads one request, or io.EOF when input closes or the
// idle limit passes with nothing to do.
func readRequestLine(ctx context.Context, lines *bufio.Reader) ([]byte, error) {
	type read struct {
		line []byte
		err  error
	}
	done := make(chan read, 1)
	go func() {
		line, err := lines.ReadBytes('\n')
		if errors.Is(err, io.EOF) && len(line) > 0 {
			err = nil
		}
		done <- read{line: line, err: err}
	}()
	idle := time.NewTimer(IdleExit)
	defer idle.Stop()
	select {
	case r := <-done:
		return r.line, r.err
	case <-idle.C:
		return nil, io.EOF
	case <-ctx.Done():
		return nil, io.EOF
	}
}

func writeResponse(output io.Writer, response Response) error {
	payload, err := surveyjson.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode scan worker response: %w", err)
	}
	if _, err := output.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("write scan worker response: %w", err)
	}
	return nil
}

func scanner(req Request) (scan.CodeScanner, error) {
	switch strings.TrimSpace(req.Impl) {
	case library.ImplOSVScalibr:
		if req.AdvisoryDatabase != "" {
			return library.NewProvisionedScalibrScanner(req.ID, req.AdvisoryDatabase), nil
		}
		return library.NewScalibrScanner(req.ID), nil
	case library.ImplGitleaks:
		var fp *secretmatch.Fingerprinter
		if len(req.FingerprintKey) > 0 {
			var err error
			fp, err = secretmatch.NewFingerprinter(req.FingerprintKey)
			if err != nil {
				return nil, fmt.Errorf("secret identity: %w", err)
			}
		}
		return library.NewGitleaksScanner(library.GitleaksOptions{ID: req.ID, Jobs: req.Jobs, Fingerprinter: fp})
	default:
		return nil, fmt.Errorf("unknown library scanner implementation %q", req.Impl)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func workerResponse(result *scanoutput.Result, err error) Response {
	response := Response{Result: result, Error: errorText(err)}
	if result != nil {
		response.SecretIdentities = result.SecretIdentities
	}
	return response
}
