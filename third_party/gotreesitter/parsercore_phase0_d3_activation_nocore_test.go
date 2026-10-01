//go:build gts_no_parsercorephase0

package gotreesitter

import "testing"

func TestG18CertificateActivationNoParserCoreIsNoOp(t *testing.T) {
	var p *Parser
	restore := p.DiagnosticEnableDropCohortCertificateAdmissionForTest()
	restore()
}
