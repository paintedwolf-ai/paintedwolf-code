package docext

import (
	"archive/zip"
	"bytes"
	"fmt"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"io"
)

// checkZipExpansion bounds package members and expansion.
func checkZipExpansion(raw []byte, bounds Bounds) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, attacherr.Unsupported("document package is not a valid zip")
	}
	if bounds.MaxBodyBytes < 0 || bounds.MaxExpansionRatio < 0 {
		return nil, attacherr.Unsupported("invalid document bounds")
	}
	maxMember := uint64(bounds.MaxBodyBytes)
	var uncompressed uint64
	for _, f := range zr.File {
		uncompressed += f.UncompressedSize64
		if maxMember > 0 && f.UncompressedSize64 > maxMember {
			return nil, attacherr.TooLarge("document member exceeds file byte cap")
		}
	}
	if len(raw) > 0 && bounds.MaxExpansionRatio > 0 {
		ratio := float64(uncompressed) / float64(len(raw))
		if ratio > float64(bounds.MaxExpansionRatio) {
			return nil, attacherr.TooLarge("document expansion ratio exceeds bound")
		}
	}
	if maxMember > 0 && bounds.MaxExpansionRatio > 0 {
		limit := maxMember * uint64(bounds.MaxExpansionRatio)
		if uncompressed > limit {
			return nil, attacherr.TooLarge("document uncompressed size exceeds bound")
		}
	}
	return zr, nil
}

func readZipFile(f *zip.File, max int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	limited := io.LimitReader(rc, max+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("member too large")
	}
	return b, nil
}
