package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/scan/installed"
)

func TestInstalledScannerCommandRequiresOneExplicitRoot(t *testing.T) {
	for _, args := range [][]string{nil, {"--root"}, {"--unknown"}, {"--root", "/tmp", "extra"}} {
		var out bytes.Buffer
		if err := runVerifyInstalledEngine(t.Context(), args, &out); err == nil || out.Len() != 0 {
			t.Fatalf("invalid arguments %v: output=%q error=%v", args, out.String(), err)
		}
	}
}

func TestInstalledScannerCommandEmitsStructuredFailure(t *testing.T) {
	var out bytes.Buffer
	if err := runVerifyInstalledEngine(t.Context(), []string{"--root", "relative"}, &out); err == nil {
		t.Fatal("relative root passed")
	}
	var report installed.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Status != "failed" || report.Code != "root_invalid" || report.Confined {
		t.Fatalf("structured result=%+v decode=%v output=%q", report, err, out.String())
	}
	if err := runVerifyInstalledEngine(t.Context(), []string{"--root", "relative"}, failingScannerWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("output failure = %v", err)
	}
}

type failingScannerWriter struct{}

func (failingScannerWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
