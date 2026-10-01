package webresearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/inboundwrite"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// fetchAssetReceipt is the JSON tool result for binary (and optional textual) writes.
type fetchAssetReceipt struct {
	OK          bool   `json:"ok"`
	Mode        string `json:"mode"`
	URL         string `json:"url"`
	Dest        string `json:"dest"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type,omitempty"`
	Status      int    `json:"status"`
	Written     bool   `json:"written"`
}

func formatAssetReceipt(res FetchRawResult, dest string, n int) (string, error) {
	sum := sha256.Sum256(res.Body)
	out := fetchAssetReceipt{
		OK:          true,
		Mode:        "raw",
		URL:         res.URL,
		Dest:        filepath.ToSlash(dest),
		Bytes:       n,
		SHA256:      hex.EncodeToString(sum[:]),
		ContentType: res.ContentType,
		Status:      res.Status,
		Written:     true,
	}
	b, err := surveyjson.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func writeFetchAsset(
	ctx context.Context,
	boundary *sandbox.Boundary,
	tctx tools.ToolContext,
	dest string,
	body []byte,
) (displayPath string, n int, err error) {
	if strings.TrimSpace(dest) == "" {
		return "", 0, &tools.ToolReject{
			Code: "FETCH_URL_DEST_REQUIRED",
			Data: map[string]any{"content_type": "unknown"},
		}
	}
	receipt, err := inboundwrite.Write(ctx, boundary, tctx, dest, body, "FETCH_URL_DEST_DENIED")
	if err != nil {
		return "", 0, err
	}
	return receipt.Path, receipt.Bytes, nil
}
