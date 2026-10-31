package mutation

import (
	"compress/gzip"
	"context"
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

// gzipInput meters compressed throughput without waiting for EOF. The decoder
// owns its small read-ahead buffer; cancellation is checked on every refill.
type gzipInput struct {
	ctx               context.Context
	reader            io.Reader
	maximum, observed int64
}

func (r *gzipInput) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	p = p[:min(int64(len(p)), max(r.maximum-r.observed+1, 1))]
	n, err := r.reader.Read(p)
	r.observed += int64(n)
	if r.observed > r.maximum {
		return max(n-1, 0), limitRefusal("archive_input_bytes", "bytes", r.maximum, r.observed)
	}
	return n, err
}

type gzipOutput struct {
	reader                               io.Reader
	input                                *gzipInput
	maximum, ratio, knownBytes, observed int64
}

func (r *gzipOutput) Read(p []byte) (int, error) {
	if err := r.input.ctx.Err(); err != nil {
		return 0, err
	}
	p = p[:min(int64(len(p)), max(r.maximum-r.observed+1, 1))]
	n, err := r.reader.Read(p)
	r.observed += int64(n)
	compressed := r.knownBytes
	if compressed < 0 {
		compressed = r.input.observed
	}
	maximum := r.maximum
	if compressed > 0 && compressed <= maximum/r.ratio {
		maximum = compressed * r.ratio
	}
	if r.observed > maximum {
		allowed := max(int64(n)-(r.observed-maximum), 0)
		return int(allowed), limitRefusal("archive_extract_bytes", "bytes", maximum, r.observed)
	}
	return n, err
}

// PATH inputs have a known compressed size. Stdin uses the compressed bytes
// read so far, so neither expansion allowance nor progress depends on EOF.
func (inv *invocation) gunzipStream(reader io.Reader, knownBytes int64) error {
	input := &gzipInput{ctx: inv.ctx, reader: reader, maximum: inv.owners.limit("archive_input_bytes")}
	gz, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	output := &gzipOutput{reader: gz, input: input, maximum: inv.owners.limit("archive_extract_bytes"), ratio: inv.owners.limit("archive_expansion_ratio"), knownBytes: knownBytes}
	_, err = io.Copy(inv.stdout, output)
	return err
}
