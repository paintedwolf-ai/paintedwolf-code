package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/diagnostics"
	"github.com/lycaon/lycaon/internal/preflight"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// diagnosticsExportFailed answers a bundle that could not be built; the cause
// stays in the log because it names local paths.
func (s *Server) diagnosticsExportFailed(w http.ResponseWriter, r *http.Request, err error) {
	s.responses.Logger.ErrorContext(r.Context(), "diagnostics export failed", "err", err)
	s.responses.Fail(w, wire.ApiErrorCodeDiagnosticsExportFailed, "the diagnostics bundle could not be built")
}

// handleExportDiagnostics writes a redacted local diagnostics bundle.
func (s *Server) handleExportDiagnostics(w http.ResponseWriter, r *http.Request) {
	redactor, err := diagnostics.NewRedactor()
	if err != nil {
		s.diagnosticsExportFailed(w, r, err)
		return
	}
	results, overall := preflight.Default().Run(r.Context(), s.preflightEnv)

	probes := make([]wire.PreflightProbe, 0, len(results))
	facts := preflightHostFacts(s.preflightEnv)
	now := time.Now()
	for _, res := range results {
		probes = append(probes, s.preflightProbeWire(res, facts, now))
	}

	raw, err := diagnostics.Build(r.Context(), diagnostics.Input{
		Health: s.healthPayload(),
		Preflight: wire.PreflightReport{
			Overall:                string(overall),
			Probes:                 probes,
			AttachmentCapabilities: attachmentCapabilitiesWire(s.Prompt.Caps),
		},
		ConfigDir:   s.dataDir,
		LogDir:      s.dataDir,
		GeneratedAt: time.Now(),
		Redactor:    redactor,
	})
	if err != nil {
		s.diagnosticsExportFailed(w, r, err)
		return
	}

	filename := fmt.Sprintf("painted-wolf-code-diagnostics-%s.zip", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
