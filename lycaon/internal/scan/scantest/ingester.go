package scantest

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
)

// DiscardIngester keeps execution fixtures independent of finding persistence.
type DiscardIngester struct{}

func (DiscardIngester) Ingest(context.Context, scan.ScanSource, *scanoutput.Result, scan.IngestMeta) (evidence.Record, error) {
	return evidence.Record{}, nil
}
