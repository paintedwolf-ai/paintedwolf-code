// Package localdata catalogs rebuildable host data.
package localdata

// Bucket IDs form a closed wire vocabulary.
const (
	BucketWebIndex           = "web_index"
	BucketSourceObservations = "source_observations"
	BucketSourceCatalog      = "source_catalog"
	BucketFetchCache         = "fetch_cache"
	BucketOSVCache           = "osv_cache"
	BucketModelfeed          = "modelfeed"
	BucketPricingCache       = "pricing_cache"
	BucketBrowserCache       = "browser_cache"
	BucketExtensionCache     = "extension_cache"
	BucketDebugLogs          = "debug_logs"
	BucketScanScratch        = "scan_scratch"
	BucketWorkerBranches     = "worker_branches"
	BucketSessionScratch     = "session_scratch"
)

// catalogOrder is the stable status and clear order.
var catalogOrder = []string{
	BucketWebIndex,
	BucketSourceObservations,
	BucketSourceCatalog,
	BucketFetchCache,
	BucketOSVCache,
	BucketModelfeed,
	BucketPricingCache,
	BucketBrowserCache,
	BucketExtensionCache,
	BucketDebugLogs,
	BucketScanScratch,
	BucketWorkerBranches,
	BucketSessionScratch,
}

// Catalog returns a copy of the closed bucket id list.
func Catalog() []string {
	out := make([]string, len(catalogOrder))
	copy(out, catalogOrder)
	return out
}

// Known reports whether id is in the closed catalog.
func Known(id string) bool {
	for _, b := range catalogOrder {
		if b == id {
			return true
		}
	}
	return false
}
