//go:build !windows

package sourceblob

func prepareObjectPublication(string) (func(), error) { return func() {}, nil }
