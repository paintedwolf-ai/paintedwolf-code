package contractfixture

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webresearch"
)

func MustCatalog(t *testing.T) *webresearch.Catalog {
	t.Helper()
	cat, err := webresearch.LoadCatalog()
	testutil.FailErr(t, "LoadCatalog", err)
	return cat
}
