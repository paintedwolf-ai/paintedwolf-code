package pricing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
)

const (
	// diskCacheFormat versions price presence and identity semantics.
	diskCacheFormat = 2
)

type diskCache struct {
	Format            int          `json:"format"`
	SourceID          string       `json:"source_id"`
	FetchedAt         time.Time    `json:"fetched_at"`
	SourceLastUpdated time.Time    `json:"source_last_updated"`
	Status            SourceStatus `json:"status"`
	Rates             []cacheRate  `json:"rates"`
}

type cacheRate struct {
	Kind    string `json:"kind"`
	ModelID string `json:"model_id"`
	Rate    Rate   `json:"rate"`
}

func cachePath(dir, id string) string {
	safe := stringsSafeFile(id)
	return filepath.Join(dir, safe+".json")
}

func stringsSafeFile(id string) string {
	b := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	if len(b) == 0 {
		return "source"
	}
	return string(b)
}

func writeDiskCache(dir, id string, table RateTable) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	dc := diskCache{
		Format:            diskCacheFormat,
		SourceID:          id,
		FetchedAt:         table.FetchedAt,
		SourceLastUpdated: table.SourceLastUpdated,
		Status:            table.Status,
		Rates:             make([]cacheRate, 0, len(table.Rates)),
	}
	for k, r := range table.Rates {
		dc.Rates = append(dc.Rates, cacheRate{Kind: k.Kind, ModelID: k.ModelID, Rate: r})
	}
	raw, err := json.MarshalIndent(dc, "", "  ")
	if err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(cachePath(dir, id)),
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}

func readDiskCache(dir, id string) (RateTable, bool, error) {
	if dir == "" {
		return RateTable{}, false, nil
	}
	raw, err := os.ReadFile(cachePath(dir, id)) // #nosec G304 -- registry-managed cache path
	if err != nil {
		if os.IsNotExist(err) {
			return RateTable{}, false, nil
		}
		return RateTable{}, false, err
	}
	var dc diskCache
	if err := json.Unmarshal(raw, &dc); err != nil {
		return RateTable{}, false, fmt.Errorf("pricing: corrupt cache for %s: %w", id, err)
	}
	if dc.Format != diskCacheFormat {
		return RateTable{}, false, nil
	}
	table := RateTable{
		Rates:             make(map[RateKey]Rate, len(dc.Rates)),
		FetchedAt:         dc.FetchedAt,
		SourceLastUpdated: dc.SourceLastUpdated,
		Status:            dc.Status,
	}
	for _, e := range dc.Rates {
		table.Rates[RateKey{Kind: RateKind(e.Kind), ModelID: e.ModelID}] = e.Rate
	}
	if err := validateRateTable(table); err != nil {
		return RateTable{}, false, fmt.Errorf("pricing: invalid cache for %s: %w", id, err)
	}
	return table, true, nil
}
