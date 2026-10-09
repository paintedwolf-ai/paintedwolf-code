package sourcecatalog

import (
	"fmt"
	"sync"
	"time"

	"github.com/lycaon/lycaon/config"
)

type treeStorePolicy struct {
	retention   time.Duration
	maxBytes    int64
	vacuumPages int
}

var treePolicyOnce sync.Once
var treePolicy treeStorePolicy

func defaultTreeStorePolicy() treeStorePolicy {
	treePolicyOnce.Do(func() {
		raw, err := config.Read(config.StorageSourceCatalog)
		if err != nil {
			panic(fmt.Errorf("source catalog retention: %w", err))
		}
		treePolicy, err = decodeTreeStorePolicy(raw)
		if err != nil {
			panic(err)
		}
	})
	return treePolicy
}

func decodeTreeStorePolicy(raw []byte) (treeStorePolicy, error) {
	var file struct {
		Catalog struct {
			Days   int   `yaml:"retention_days"`
			Bytes  int64 `yaml:"max_total_bytes"`
			Vacuum int   `yaml:"vacuum_pages"`
		} `yaml:"source_catalog"`
	}
	if err := config.DecodeYAML(raw, &file); err != nil {
		return treeStorePolicy{}, fmt.Errorf("source catalog retention: %w", err)
	}
	if file.Catalog.Days <= 0 || file.Catalog.Bytes <= 0 || file.Catalog.Vacuum <= 0 {
		return treeStorePolicy{}, fmt.Errorf("source catalog retention requires positive age, byte and vacuum budgets")
	}
	return treeStorePolicy{retention: time.Duration(file.Catalog.Days) * 24 * time.Hour, maxBytes: file.Catalog.Bytes, vacuumPages: file.Catalog.Vacuum}, nil
}
