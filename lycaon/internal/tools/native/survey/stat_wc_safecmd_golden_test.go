package survey

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

func TestStatLiteralSacredGolden(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "run.sh")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))

	out, err := (&StatTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"paths": []any{"run.sh"},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "stat", err)
	if _, ok := surveyreceipt.Parse(out); !ok {
		t.Fatalf("missing receipt: %q", out)
	}

	var gotResp statResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &gotResp))
	gotRaw, err := surveyjson.Marshal(gotResp)
	testutil.FailErr(t, "marshal got", err)

	info, err := os.Lstat(path)
	testutil.FailErr(t, "lstat", err)
	wantResp := statResponse{Results: []statResult{statEntryFromInfo("run.sh", info)}}
	wantRaw, err := surveyjson.Marshal(wantResp)
	testutil.FailErr(t, "marshal want", err)
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("stat JSON drift:\ngot  %s\nwant %s", gotRaw, wantRaw)
	}
}

func TestWcLiteralSacredGolden(t *testing.T) {
	tmpDir := t.TempDir()
	content := "line1\nline2\nline3\n"
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(tmpDir, "foo.go"), []byte(content), 0o644))

	out, err := (&WcTool{Boundary: nativefixture.Boundary(t)}).Run(context.Background(), map[string]any{
		"paths": []any{"foo.go"},
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "wc", err)
	if _, ok := surveyreceipt.Parse(out); !ok {
		t.Fatalf("missing receipt: %q", out)
	}

	var gotResp wcResponse
	testutil.FailErr(t, "decode", json.Unmarshal([]byte(out), &gotResp))
	gotRaw, err := surveyjson.Marshal(gotResp)
	testutil.FailErr(t, "marshal got", err)

	lines := 3
	words := 3
	wantResp := wcResponse{Results: []wcResult{{
		Path:  "foo.go",
		Lines: &lines,
		Bytes: int64(len(content)),
		Words: &words,
	}}}
	wantRaw, err := surveyjson.Marshal(wantResp)
	testutil.FailErr(t, "marshal want", err)
	if string(gotRaw) != string(wantRaw) {
		t.Fatalf("wc JSON drift:\ngot  %s\nwant %s", gotRaw, wantRaw)
	}
}

func TestStatWcCapsComeFromSafecmd(t *testing.T) {
	if safecmd.StatMaxPaths != 50 || safecmd.StatCaps().ResultCount != safecmd.StatMaxPaths {
		t.Fatalf("stat caps = %d / %#v", safecmd.StatMaxPaths, safecmd.StatCaps())
	}
	if safecmd.WCMaxFiles != 50 || safecmd.WCMaxRecursiveFiles != 500 {
		t.Fatalf("wc caps = %d / %d", safecmd.WCMaxFiles, safecmd.WCMaxRecursiveFiles)
	}
}
