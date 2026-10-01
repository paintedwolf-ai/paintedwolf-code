package scan

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

// ScanResultSpillDir is the path-keyed host-data subdirectory for oversized result_json.
const ScanResultSpillDir = "scan-results"

// SpillResultJSON stores oversized bodies and returns a host-only result stub.
func SpillResultJSON(result *scanoutput.Result, capBytes int, spillDir, scanID string) ([]byte, error) {
	if result == nil {
		return json.Marshal((*scanoutput.Result)(nil))
	}
	if capBytes <= 0 {
		return json.Marshal(result)
	}
	tmp, err := os.CreateTemp("", "paintedwolf-scan-result-*.json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()
	if err := json.NewEncoder(tmp).Encode(result); err != nil {
		return nil, err
	}
	info, err := tmp.Stat()
	if err != nil {
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if info.Size() <= int64(capBytes) {
		return io.ReadAll(tmp)
	}
	scanID = strings.TrimSpace(scanID)
	if scanID == "" {
		return nil, fmt.Errorf("scan result spill: scan id required")
	}
	if err := os.MkdirAll(spillDir, 0o700); err != nil {
		return nil, err
	}
	fileName := scanID + ".json"
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: spillDir, Rel: fileName},
		Source:   tmp,
		Mode:     0o600,
		DirMode:  0o700,
	}); err != nil {
		return nil, err
	}
	rel := filepath.ToSlash(filepath.Join(ScanResultSpillDir, fileName))
	stub := &scanoutput.Result{
		FindingsCount:    result.FindingsCount,
		Categories:       append([]api.ScanCategory(nil), result.Categories...),
		Warnings:         append([]api.ScanWarning(nil), result.Warnings...),
		ResultSpillPath:  rel,
		ResultSpillBytes: int(info.Size()),
	}
	return json.Marshal(stub)
}

// LoadSpilledResult reads a spilled full ScanResult from evidenceRoot + relative spill path.
func LoadSpilledResult(evidenceRoot, spillRel string) (*scanoutput.Result, error) {
	spillRel = strings.TrimSpace(spillRel)
	evidenceRoot = strings.TrimSpace(evidenceRoot)
	if spillRel == "" || evidenceRoot == "" {
		return nil, fmt.Errorf("spill path and evidence root required")
	}
	abs := filepath.Join(evidenceRoot, filepath.FromSlash(spillRel))
	raw, err := os.ReadFile(abs) // #nosec G304 -- host-managed spill under PathKeyedHostDataDir
	if err != nil {
		return nil, err
	}
	var out scanoutput.Result
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
