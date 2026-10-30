package sourcecatalog

// invalidateContent advances preparation without treating eviction as a write.
func (c *TreeStores) invalidateContent(rootPath string) {
	c.mu.Lock()
	var stores []*indexStore
	for _, store := range c.trees {
		if index, ok := store.(*indexStore); ok && cleanAbs(index.root.Path) == rootPath {
			stores = append(stores, index)
		}
	}
	c.mu.Unlock()
	for _, store := range stores {
		store.mu.Lock()
		store.invalidateContentLocked()
		store.mu.Unlock()
	}
}

func (s *indexStore) invalidateContentLocked() {
	for _, build := range s.content {
		build.invalidated = true
	}
}
