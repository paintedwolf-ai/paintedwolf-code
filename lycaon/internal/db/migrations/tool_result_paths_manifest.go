package migrations

var toolResultPathsSource = Baseline{Revision: 3, Shape: "c0280798481d97c01e3ddf30812db8b6c28db69520cd8b33b88642ddb03d18a2"}

// ToolResultPaths restores target lookup for recorded tool observations.
func ToolResultPaths(target Baseline) Step {
	return Step{
		ID: "recorded-tool-result-paths", Source: toolResultPathsImplementation,
		Checksum: "82e7d68c1666f746a134fa00da43e5130b5dc7ff98b1e72b33cd082664c2b178",
		From:     toolResultPathsSource, To: target, Apply: migrateToolResultPaths,
	}
}
