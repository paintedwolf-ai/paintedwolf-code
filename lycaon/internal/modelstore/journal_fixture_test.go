package modelstore

import "path/filepath"

func (s *Store) journalPath() string { return filepath.Join(s.root, journalFileName) }
