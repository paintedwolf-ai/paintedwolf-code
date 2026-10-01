package extpacks

// DiscoverAllContent returns stock and locked package content.
func DiscoverAllContent(projectDirs []string) ([]PackContent, error) {
	release, err := AcquireIntentLocks(projectDirs)
	if err != nil {
		return nil, err
	}
	defer release()
	return discoverAllContent(projectDirs)
}

func discoverAllContent(projectDirs []string) ([]PackContent, error) {
	stock, err := DiscoverStockContent()
	if err != nil {
		return nil, err
	}
	locked, err := discoverLockedContent(projectDirs)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]PackContent, len(stock)+len(locked))
	order := make([]string, 0, len(stock)+len(locked))
	add := func(content PackContent) {
		id := content.Pack.ID
		// Bundled bytes take precedence over disk declarations.
		if incumbent, exists := byID[id]; exists {
			if incumbent.Pack.Root.IsBundled() && !content.Pack.Root.IsBundled() {
				return
			}
		} else {
			order = append(order, id)
		}
		byID[id] = content
	}
	for _, content := range stock {
		add(content)
	}
	for _, content := range locked {
		add(content)
	}
	out := make([]PackContent, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out, nil
}
