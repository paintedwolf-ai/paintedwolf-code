package sourcecatalog

import "context"

// IndexCoverage describes omissions in the same generation as its file rows.
type IndexCoverage struct {
	DiscoveryComplete  bool
	Refreshing         bool
	BoundedDirectories int
	FailedDirectories  int
	Error              string
}

func CoverageFromStatus(status TreeStatus) IndexCoverage {
	return IndexCoverage{DiscoveryComplete: status.Complete, Refreshing: status.Refreshing, Error: status.Error}
}

// Pending excludes failed builds, which need an explicit retry rather than polling.
func (c IndexCoverage) Pending() bool {
	return c.Error == "" && (!c.DiscoveryComplete || c.Refreshing)
}

func (c IndexCoverage) Exhaustive() bool {
	return c.DiscoveryComplete && !c.Refreshing && c.Error == "" && c.BoundedDirectories == 0 && c.FailedDirectories == 0
}

func (r *IndexReader) Coverage(ctx context.Context) (IndexCoverage, error) {
	coverage := CoverageFromStatus(r.Status)
	err := r.tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM nodes WHERE refused<>''),
		(SELECT count(*) FROM faults)`).Scan(&coverage.BoundedDirectories, &coverage.FailedDirectories)
	r.RowsRead++
	return coverage, err
}
