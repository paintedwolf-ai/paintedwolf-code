package api

// FindingExportFormat names an export document's shape.
type FindingExportFormat string

const (
	// FindingExportSARIF renders matched findings as SARIF 2.1 with ignores as suppressions.
	FindingExportSARIF FindingExportFormat = "sarif"
	// FindingExportOpenVEX renders advisory findings as an OpenVEX document.
	FindingExportOpenVEX FindingExportFormat = "openvex"
)
