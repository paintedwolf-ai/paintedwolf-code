package wirespec

// LoadOpenAPIOperationIDs returns every operationId the bundled spec declares.
func LoadOpenAPIOperationIDs(repoRoot string) ([]string, error) {
	routes, err := LoadOpenAPIRoutes(repoRoot)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range routes {
		if r.OperationID != "" {
			ids = append(ids, r.OperationID)
		}
	}
	return ids, nil
}
