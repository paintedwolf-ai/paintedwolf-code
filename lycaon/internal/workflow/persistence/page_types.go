package persistence

import (
	"errors"

	"github.com/lycaon/lycaon/internal/pagecursor"
)

var workflowRunPages = pagecursor.For[workflowRunPageCursor]("workflow_runs")
var ErrInvalidRunPageCursor = errors.New("invalid workflow run page cursor")

type workflowRunPageCursor struct {
	WatermarkOrdinal int64  `json:"watermark_ordinal"`
	BeforeCreatedAt  string `json:"before_created_at"`
	BeforeID         string `json:"before_id"`
}
