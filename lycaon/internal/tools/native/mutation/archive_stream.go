package mutation

import (
	"bytes"
	"compress/gzip"
	"io"
)

// boundedReader refuses decoded bytes past the expansion ceiling. Bytes
// returned before refusal remain a valid partial stream, never a complete one.
type boundedReader struct {
	r                 io.Reader
	maximum, observed int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	p = p[:min(int64(len(p)), max(b.maximum-b.observed+1, 1))]
	n, err := b.r.Read(p)
	b.observed += int64(n)
	if b.observed > b.maximum {
		return max(n-1, 0), limitRefusal("archive_extract_bytes", "bytes", b.maximum, b.observed)
	}
	return n, err
}

func (inv *invocation) gunzipStream(data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	_, err = io.Copy(inv.stdout, &boundedReader{r: gz, maximum: inv.decodeLimit(int64(len(data)))})
	return err
}
