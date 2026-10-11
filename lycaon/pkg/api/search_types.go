package api

// Search wire types for global federated search.

type SearchScopeMode string

const (
	SearchScopeGlobal  SearchScopeMode = "global"
	SearchScopeCurrent SearchScopeMode = "current"
	SearchScopeSlug    SearchScopeMode = "slug"
)

// SearchBudget is the crawl budget for one federated search.
type SearchBudget string

const (
	SearchBudgetComplete    SearchBudget = "complete"
	SearchBudgetInteractive SearchBudget = "interactive"
)

type SearchResultStatus string

const (
	SearchResultStatusComplete SearchResultStatus = "complete"
	SearchResultStatusLimited  SearchResultStatus = "limited"
	SearchResultStatusPartial  SearchResultStatus = "partial"
)

type SearchCountRelation string

const (
	SearchCountRelationExact      SearchCountRelation = "exact"
	SearchCountRelationLowerBound SearchCountRelation = "lower_bound"
)

type SearchReplacePreviewState string

const (
	SearchReplacePreviewPreparing SearchReplacePreviewState = "preparing"
	SearchReplacePreviewReady     SearchReplacePreviewState = "ready"
	SearchReplacePreviewLimited   SearchReplacePreviewState = "limited"
)

type SearchIssueReason string

const (
	SearchIssueReasonResultLimit   SearchIssueReason = "result_limit"
	SearchIssueReasonExecutorError SearchIssueReason = "executor_error"
	SearchIssueReasonFilesSkipped  SearchIssueReason = "files_skipped"
	// SearchIssueReasonTimeBudget: a source leg's wall-clock budget ended with
	// files unscanned.
	SearchIssueReasonTimeBudget SearchIssueReason = "time_budget"
	// SearchIssueReasonCatalogWarming: Count roots had no source generation in
	// time; the build they started continues.
	SearchIssueReasonCatalogWarming SearchIssueReason = "catalog_warming"
	// SearchIssueReasonCatalogIncomplete: Count roots answered from a
	// generation that does not yet cover the tree; more hits arrive as
	// discovery continues.
	SearchIssueReasonCatalogIncomplete SearchIssueReason = "catalog_incomplete"
	// SearchIssueReasonCatalogBounded: a walk budget refused Count directories,
	// so their files were never indexed. Asking again does not resolve it.
	SearchIssueReasonCatalogBounded       SearchIssueReason = "catalog_bounded"
	SearchIssueReasonIndexWarming         SearchIssueReason = "index_warming"
	SearchIssueReasonCatalogFailed        SearchIssueReason = "catalog_failed"
	SearchIssueReasonCatalogRefreshFailed SearchIssueReason = "catalog_refresh_failed"
	SearchIssueReasonCatalogRefreshing    SearchIssueReason = "catalog_refreshing"
	// SearchIssueReasonSymbolPending: retrying advances retained declaration work.
	SearchIssueReasonSymbolPending SearchIssueReason = "symbol_pending"
	// SearchIssueReasonSymbolBudget: a terminal declaration resource bound.
	SearchIssueReasonSymbolBudget SearchIssueReason = "symbol_budget"
)

type SearchExportFormat string

const (
	SearchExportFormatJSONL SearchExportFormat = "jsonl"
	SearchExportFormatCSV   SearchExportFormat = "csv"
	SearchExportFormatSARIF SearchExportFormat = "sarif"
)

// SearchReplaceHunkContext is the syntactic context of a replace match start.
type SearchReplaceHunkContext string

const (
	SearchReplaceContextCode            SearchReplaceHunkContext = "code"
	SearchReplaceContextCommentOrString SearchReplaceHunkContext = "comment_or_string"
)
