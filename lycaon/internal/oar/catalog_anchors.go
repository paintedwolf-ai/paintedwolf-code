package oar

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
)

// RequireKnownCatalogAnchor validates an anchor against the installed catalog.
func RequireKnownCatalogAnchor(anchor string) error {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return fmt.Errorf("oar: empty anchor (expected a catalog anchor id)")
	}
	if err := anchorcatalog.Require(anchor); err != nil {
		if !anchorcatalog.Loaded() {
			return fmt.Errorf("oar: catalog anchors not installed (load anchors/catalog.yaml before hints)")
		}
		return fmt.Errorf("oar: unknown anchor %q (not in anchors/catalog.yaml)", anchor)
	}
	return nil
}
