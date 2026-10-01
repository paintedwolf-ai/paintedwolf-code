// Package oswalk holds the one nilerr suppression for filepath.WalkDir
// callbacks that continue past a failing entry.
package oswalk

// Skip discards a per-entry walk error and returns nil so filepath.WalkDir
// continues. Callers pass the error they are dropping.
func Skip(err error) error {
	_ = err
	return nil
}
