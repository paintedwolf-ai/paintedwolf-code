package search

import "github.com/lycaon/lycaon/internal/sourcecatalog"

func (r *CodeLegReport) observeCoverage(c sourcecatalog.IndexCoverage) {
	r.Roots++
	r.UnobservedDirs += c.BoundedDirectories
	r.FailedDirs += c.FailedDirectories
	if c.Error != "" {
		r.RefreshFailedRoots++
		return
	}
	if !c.DiscoveryComplete {
		r.IncompleteRoots++
	} else if c.Refreshing {
		r.RefreshingRoots++
	}
}

// CoverageIssues preserves every reason an answer cannot establish absence.
func (r ExecutorReport) CoverageIssues(executor string) []Issue {
	issues := append([]Issue(nil), r.Issues...)
	counts := []struct {
		reason IssueReason
		count  int
	}{
		{IssueFilesSkipped, r.SkippedFiles},
		{IssueCatalogWarming, r.Code.WarmingRoots},
		{IssueCatalogIncomplete, r.Code.IncompleteRoots},
		{IssueCatalogRefreshing, r.Code.RefreshingRoots},
		{IssueCatalogBounded, r.Code.UnobservedDirs},
		{IssueCatalogFailed, r.Code.FailedDirs},
		{IssueCatalogRefreshFailed, r.Code.RefreshFailedRoots},
	}
	for _, item := range counts {
		if item.count > 0 {
			issues = append(issues, Issue{Executor: executor, Reason: item.reason, Count: item.count})
		}
	}
	if r.TimedOut {
		issues = append(issues, Issue{Executor: executor, Reason: IssueTimeBudget})
		if r.Code.IndexWarmingRoots > 0 {
			issues = append(issues, Issue{Executor: executor, Reason: IssueIndexWarming, Count: r.Code.IndexWarmingRoots})
		}
	}
	return issues
}
