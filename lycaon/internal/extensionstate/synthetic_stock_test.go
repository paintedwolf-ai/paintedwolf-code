package extensionstate_test

import (
	"io/fs"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
)

// syntheticStock is the stock the mutation tests resolve against: the
// platform pack every catalog builds on and two small packs. Validating a
// candidate against it skips the shipped catalog's other ten packs.
var syntheticStock = []string{"platform", "options", "themes"}

// useSyntheticStock stages the bundled tree with only syntheticStock under the
// stock namespace and a suite manifest that names just those members.
func useSyntheticStock(t *testing.T) {
	t.Helper()
	var members strings.Builder
	for _, leaf := range syntheticStock {
		members.WriteString("  - painted-wolf/" + leaf + "\n")
	}
	meta := "manifest_version: 1\nid: painted-wolf/stock\nname: Painted Wolf stock\nversion: \"1.0.0\"\n" +
		"compatibility:\n  extension_api: \"^1.0.0\"\nmembers:\n" + members.String() + "conflicts_with: []\nextends: []\n"
	configtest.Use(t, stockSubset{
		base: config.Source(),
		meta: fstest.MapFS{config.StockMeta.String(): &fstest.MapFile{Data: []byte(meta), Mode: 0o644}},
	})
}

// stockSubset hides every stock pack outside syntheticStock and serves the
// synthetic suite manifest.
type stockSubset struct {
	base fs.FS
	meta fstest.MapFS
}

func (s stockSubset) hidden(name string) bool {
	rest, ok := strings.CutPrefix(name, config.StockPacks.String()+"/")
	if !ok {
		return false
	}
	leaf, _, _ := strings.Cut(rest, "/")
	return path.Ext(leaf) == "" && !slices.Contains(syntheticStock, leaf)
}

func (s stockSubset) Open(name string) (fs.File, error) {
	if name == config.StockMeta.String() {
		return s.meta.Open(name)
	}
	if s.hidden(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return s.base.Open(name)
}

func (s stockSubset) ReadDir(name string) ([]fs.DirEntry, error) {
	if s.hidden(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	entries, err := fs.ReadDir(s.base, name)
	if err != nil || name != config.StockPacks.String() {
		return entries, err
	}
	return slices.DeleteFunc(entries, func(e fs.DirEntry) bool { return s.hidden(path.Join(name, e.Name())) }), nil
}

func (s stockSubset) Stat(name string) (fs.FileInfo, error) {
	if name == config.StockMeta.String() {
		return fs.Stat(s.meta, name)
	}
	if s.hidden(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return fs.Stat(s.base, name)
}
