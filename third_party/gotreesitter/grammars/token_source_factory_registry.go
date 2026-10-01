package grammars

import "github.com/odvcencio/gotreesitter"

var tokenSourceFactories = map[string]func(src []byte, lang *gotreesitter.Language) gotreesitter.TokenSource{}

func registerTokenSourceFactory(name string, factory func(src []byte, lang *gotreesitter.Language) gotreesitter.TokenSource) {
	tokenSourceFactories[name] = factory

	// Subset registry files and subset factory files are independently gated.
	// Keep registration correct regardless of their init order by updating an
	// entry that was already installed before its factory became available.
	registryMu.Lock()
	defer registryMu.Unlock()
	for i := range registry {
		if registry[i].Name == name {
			registry[i].TokenSourceFactory = factory
			return
		}
	}
}

func defaultTokenSourceFactory(name string) func(src []byte, lang *gotreesitter.Language) gotreesitter.TokenSource {
	return tokenSourceFactories[name]
}
