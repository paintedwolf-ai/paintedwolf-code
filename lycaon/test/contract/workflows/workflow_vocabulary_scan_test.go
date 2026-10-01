package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/vocabulary"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestScanDomainShippedInCatalog(t *testing.T) {
	t.Parallel()
	seen := map[string]vocabulary.CatalogEntry{}
	for _, row := range vocabulary.ExportCatalog() {
		if row.Domain == "scan" {
			seen[row.ID] = row
		}
	}
	for _, id := range conditions.ShippedScanDomainIDs() {
		row, ok := seen[id]
		if !ok {
			t.Fatalf("missing shipped scan domain catalog row for %q", id)
		}
		if row.Status != "shipped" || row.Layer != "domain" {
			t.Fatalf("scan domain %q catalog = %+v", id, row)
		}
	}
	for _, id := range conditions.ScanCatalogStubIDs() {
		row, ok := seen[id]
		if !ok {
			t.Fatalf("missing scan catalog stub row for %q", id)
		}
		if row.Status != "catalog" || row.Layer != "domain" {
			t.Fatalf("scan catalog stub %q = %+v", id, row)
		}
	}
}

func TestNoSecurityGateIDsInRegistry(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	for _, forbidden := range []string{
		"security_gate_passed",
		"security_gate_failed",
		"security_gate_missing",
	} {
		if reg.Has(forbidden) {
			t.Fatalf("forbidden id %q registered in condition registry", forbidden)
		}
	}
}
