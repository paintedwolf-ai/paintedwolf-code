package migrations

var toolResultPathsSource = Baseline{Revision: 3, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

// ToolResultPaths restores target lookup for recorded tool observations.
func ToolResultPaths(target Baseline) Step {
	return Step{
		ID: "recorded-tool-result-paths", Source: toolResultPathsImplementation,
		Checksum: "7d6a151e39425186d68ebc7677aec8a15c1458136317ce508a7509a81d376c28",
		From:     toolResultPathsSource, To: target, Apply: migrateToolResultPaths,
	}
}
