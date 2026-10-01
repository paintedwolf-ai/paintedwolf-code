package survey

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildReadOutlineResponseSymbolsKind(t *testing.T) {
	resp := buildReadOutlineResponse("main.go", fileoutline.Result{
		Path:       "main.go",
		TotalLines: 10,
		Source:     "tree_sitter",
		Language:   "go",
		Symbols: []fileoutline.Symbol{
			{Kind: "function", Name: "main", Line: 3},
		},
	})
	if resp.OutlineKind != api.OutlineKindSymbols {
		t.Fatalf("outline_kind = %q want symbols", resp.OutlineKind)
	}
	raw, err := json.Marshal(resp)
	testutil.FailErr(t, "json.Marshal failed", err)
	body := string(raw)
	if strings.Contains(body, "log_digest") {
		t.Fatalf("symbols outline must omit log_digest: %s", body)
	}
	if !strings.Contains(body, `"symbols"`) {
		t.Fatalf("symbols missing: %s", body)
	}
}

func TestReadResponseMarshalsLogDigestWhenSet(t *testing.T) {
	resp := ReadResponse{
		Path:          "var/log/app.log",
		Mode:          "outline",
		OutlineKind:   api.OutlineKindLogDigest,
		OutlineSource: "log",
		TotalLines:    100,
		LogDigest: &logoutline.Digest{
			Format:      logoutline.FormatJSONLines,
			RecordCount: 100,
			ParsedCount: 98,
		},
	}
	raw, err := json.Marshal(resp)
	testutil.FailErr(t, "json.Marshal failed", err)
	body := string(raw)
	for _, want := range []string{
		`"outline_kind":"log_digest"`,
		`"log_digest"`,
		`"format":"json_lines"`,
		`"record_count":100`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, `"symbols"`) {
		t.Fatalf("log digest outline should not include symbols: %s", body)
	}
}

func TestApplyLogOutlineFields(t *testing.T) {
	digest := &logoutline.Digest{Format: logoutline.FormatLogfmt, RecordCount: 5}
	resp := ReadResponse{Path: "app.log", Mode: "outline", TotalLines: 5}
	applyLogOutlineFields(&resp, fileoutline.Result{LogDigest: digest})
	if resp.OutlineKind != api.OutlineKindLogDigest || resp.LogDigest != digest || resp.OutlineSource != "log" {
		t.Fatalf("resp = %+v", resp)
	}
}
