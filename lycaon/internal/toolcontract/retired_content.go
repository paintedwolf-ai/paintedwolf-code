package toolcontract

// RetiredToContent reports the content contract that replaced a retired tool.
func RetiredToContent(name string) (string, bool) {
	contract, ok := compiledRetiredToContent[name]
	return contract, ok
}
