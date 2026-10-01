package toolapi

import (
	"context"
	"github.com/lycaon/lycaon/internal/bundledhint"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const listScansEmptyHintCode = "SCAN_LIST_EMPTY"

// ListScansResponse is the list_scans tool payload (never a bare array).
type ListScansResponse struct {
	Scans    []wire.CodeScan `json:"scans"`
	Count    int             `json:"count"`
	HintCode string          `json:"hint_code,omitempty"`
	Hint     string          `json:"hint,omitempty"`
}

// NewListScansResponse builds the list_scans envelope with an empty-state hint when needed.
func NewListScansResponse(ctx context.Context, scans []wire.CodeScan) ListScansResponse {
	if scans == nil {
		scans = []wire.CodeScan{}
	}
	resp := ListScansResponse{
		Scans: scans,
		Count: len(scans),
	}
	if len(scans) == 0 {
		resp.HintCode = listScansEmptyHintCode
		resp.Hint = bundledhint.Message(ctx, listScansEmptyHintCode, nil)
	}
	return resp
}
