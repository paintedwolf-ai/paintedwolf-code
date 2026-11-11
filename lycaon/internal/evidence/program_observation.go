package evidence

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// ExchangeProvenance summarizes the external provenance of one completed HTTP exchange.
type ExchangeProvenance struct {
	External     bool
	ObservedURLs []string
	WroteStdout  bool
	WroteStderr  bool
	WrittenFiles []string
}

type exchangeProvenanceObserverKey struct{}

// WithExchangeProvenanceObserver attaches a callback to ctx that receives provenance
// for each HTTP exchange performed within that context.
func WithExchangeProvenanceObserver(ctx context.Context, fn func(ExchangeProvenance)) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	prev, _ := ctx.Value(exchangeProvenanceObserverKey{}).(func(ExchangeProvenance))
	if prev != nil {
		combined := func(p ExchangeProvenance) {
			prev(p)
			fn(p)
		}
		return context.WithValue(ctx, exchangeProvenanceObserverKey{}, combined)
	}
	return context.WithValue(ctx, exchangeProvenanceObserverKey{}, fn)
}

// ReportExchangeProvenance notifies any observer attached to ctx.
func ReportExchangeProvenance(ctx context.Context, p ExchangeProvenance) {
	if ctx == nil {
		return
	}
	if fn, ok := ctx.Value(exchangeProvenanceObserverKey{}).(func(ExchangeProvenance)); ok && fn != nil {
		fn(p)
	}
}

// BuildProgramObservation captures execution output without inferring source access.
func BuildProgramObservation(tool string, receipt *api.CommandProgramReceipt, content string) Record {
	if receipt == nil || strings.TrimSpace(receipt.ID) == "" || strings.TrimSpace(receipt.ParentReceiptID) == "" {
		return Record{}
	}
	if tool != "command_output" && tool != "command_stop" && !toolcontract.ProgramResultTool(tool) {
		return Record{}
	}
	rec := Record{Kind: "command", Shape: ShapeCommand, SourceTool: tool, Fidelity: FidelityOpaque}
	body := fmt.Sprintf("program_id: %s\nstate: %s", receipt.ID, receipt.State)
	if receipt.StatusCode != nil {
		body += fmt.Sprintf("\nstatus_code: %d", *receipt.StatusCode)
	}
	captureRecordBody(&rec, body+"\n\n"+content)
	return rec
}

// ProgramHTTPProvenance describes external HTTP deliveries and file-custody writes
// observed during a command program run.
type ProgramHTTPProvenance struct {
	ProgramID        string
	UntrustedContent bool
	ObservedURLs     []string
	CustodyPaths     []string
	ClearedPaths     []string
}

// BuildProgramHTTPRecord constructs an http_response ledger record for a command
// program that observed external HTTP traffic or wrote external custody files.
func BuildProgramHTTPRecord(projectDir string, prov ProgramHTTPProvenance) Record {
	if !prov.UntrustedContent && len(prov.ObservedURLs) == 0 && len(prov.CustodyPaths) == 0 {
		return Record{}
	}
	rec := Record{
		Kind:             "http_response",
		Shape:            ShapeSurfaceSnapshot,
		Fidelity:         FidelityStructured,
		Surface:          SurfaceHTTP,
		SourceTool:       "command",
		UntrustedContent: prov.UntrustedContent,
		Custody:          FileCustody{External: len(prov.CustodyPaths) > 0},
	}
	for _, u := range prov.ObservedURLs {
		rec.touchURL(u)
	}
	for _, p := range prov.CustodyPaths {
		addRecordPath(projectDir, &rec, p, FidelityStructured)
	}
	if strings.TrimSpace(prov.ProgramID) != "" {
		captureRecordBody(&rec, "program_id: "+strings.TrimSpace(prov.ProgramID))
	}
	return rec
}

// BuildProgramCustodyReplacementRecord constructs a whole-file local write record
// when a command program overwrites or removes paths that previously held external custody.
func BuildProgramCustodyReplacementRecord(projectDir string, prov ProgramHTTPProvenance) Record {
	if len(prov.ClearedPaths) == 0 {
		return Record{}
	}
	rec := Record{
		Kind:       "write",
		Shape:      ShapeStructuredEvent,
		Fidelity:   FidelityStructured,
		SourceTool: "command",
	}
	for _, p := range prov.ClearedPaths {
		addRecordPath(projectDir, &rec, p, FidelityStructured)
	}
	if strings.TrimSpace(prov.ProgramID) != "" {
		captureRecordBody(&rec, "program_id: "+strings.TrimSpace(prov.ProgramID))
	}
	return rec
}

