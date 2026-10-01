package promptattach

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/promptattach/format"
)

// Receipt identifies a materialized attachment.
type Receipt struct {
	BlobID   string
	Filename string
	MIME     string
	Kind     format.Kind
	Bytes    int64
	// Video is set for a video the decoder confirmed plays.
	Video *VideoFacts
}

// expansionFloor absorbs small-stream ratio noise.
const expansionFloor = 1 << 20

// Upload streams one bounded attachment into the blob store. A video is decoded once here,
// so one the browser cannot play is refused before it reaches a prompt.
func Upload(ctx context.Context, store blobstore.Store, caps Caps, video VideoDecoder, filename, reportedMIME string, body io.Reader) (Receipt, error) {
	if !store.Available() {
		return Receipt{}, attacherr.Unsupported("attachment storage is unavailable for this project")
	}
	name := blobstore.SafeName(filename)
	counted := &countingReader{r: io.LimitReader(body, caps.Transport.MaxUpload.Int64()+1)}

	var reader io.Reader = counted
	var closers []io.Closer
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()

	detected := format.Format{}
	for depth := 0; ; depth++ {
		buffered := bufio.NewReaderSize(reader, format.PeekBytes)
		peek, err := buffered.Peek(format.PeekBytes)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
			return Receipt{}, attacherr.Unsupported(fmt.Sprintf("attachment could not be read: %v", err))
		}
		if len(peek) == 0 {
			return Receipt{}, attacherr.Unsupported("attachment content is empty")
		}
		detected = format.Detect(name, reportedMIME, peek)
		reader = buffered
		if detected.Kind != format.KindContainer {
			break
		}
		if depth >= caps.Materialization.MaxUnwrapDepth {
			return Receipt{}, attacherr.TooLarge("attachment nests compression deeper than the host unwraps")
		}
		decoded, err := detected.Container.Decode(buffered)
		if err != nil {
			return Receipt{}, attacherr.Unsupported(fmt.Sprintf("compressed attachment could not be decoded: %v", err))
		}
		closers = append(closers, decoded)
		name = blobstore.SafeName(detected.Container.InnerName(name))
		// The decoded body has its own media type.
		reportedMIME = ""
		reader = &expansionGuard{
			decoded: decoded,
			source:  counted,
			ratio:   int64(caps.Materialization.MaxExpansionRatio),
		}
	}

	if !detected.Terminal() {
		return Receipt{}, attacherr.Unsupported(unsupportedMessage(detected))
	}

	blob, err := store.Put(name, reader, caps.Materialization.MaxBody)
	if err != nil {
		return Receipt{}, materializeError(name, err)
	}
	if counted.n > caps.Transport.MaxUpload.Int64() {
		_, _ = store.DiscardStagedBefore(blob.ID, time.Time{})
		return Receipt{}, attacherr.TooLarge("attachment exceeds the upload transport bound")
	}
	if detected.Kind == format.KindText {
		if err := validateStoredUTF8(store, blob); err != nil {
			_, _ = store.DiscardStagedBefore(blob.ID, time.Time{})
			return Receipt{}, err
		}
	}
	receipt := Receipt{
		BlobID:   blob.ID,
		Filename: blob.Name,
		MIME:     detected.MIME,
		Kind:     detected.Kind,
		Bytes:    blob.Size,
	}
	if detected.Kind == format.KindVideo {
		overview, err := deriveVideoOverview(ctx, store, caps, video, blob, detected.MIME)
		if err != nil {
			_, _ = store.DiscardStagedBefore(blob.ID, time.Time{})
			return Receipt{}, err
		}
		receipt.Video = &overview.VideoFacts
	}
	return receipt, nil
}

func validateStoredUTF8(store blobstore.Store, blob blobstore.Blob) error {
	f, err := store.Open(blob)
	if err != nil {
		return attacherr.Unsupported(fmt.Sprintf("attachment %q could not be read", blob.Name))
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReader(f)
	for {
		r, size, err := reader.ReadRune()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return attacherr.Unsupported(fmt.Sprintf("attachment %q could not be read", blob.Name))
		}
		if r == utf8.RuneError && size == 1 {
			return attacherr.Unsupported(fmt.Sprintf("attachment %q is not valid UTF-8 text", blob.Name))
		}
	}
}

func unsupportedMessage(f format.Format) string {
	if f.Kind == format.KindContainer {
		return "attachment is an archive of several files; attach the file you mean, or unpack it in the workspace"
	}
	return fmt.Sprintf("attachment type %s is not supported", f.MIME)
}

func materializeError(name string, err error) error {
	var tooLarge *blobstore.TooLargeError
	if errors.As(err, &tooLarge) {
		return attacherr.TooLarge(fmt.Sprintf("%q exceeds the attachment materialization bound", name))
	}
	if errors.Is(err, errExpansion) {
		return attacherr.TooLarge(fmt.Sprintf("%q decompresses far beyond its compressed size", name))
	}
	if errors.Is(err, blobstore.ErrNoStore) {
		return attacherr.Unsupported("attachment storage is unavailable for this project")
	}
	return err
}

// countingReader tracks compressed input bytes.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

var errExpansion = errors.New("attachment expansion ratio exceeded")

// expansionGuard bounds decoded bytes against consumed input.
type expansionGuard struct {
	decoded io.Reader
	source  *countingReader
	ratio   int64
	written int64
}

func (g *expansionGuard) Read(p []byte) (int, error) {
	n, err := g.decoded.Read(p)
	g.written += int64(n)
	if g.ratio > 0 && g.written > expansionFloor+g.source.n*g.ratio {
		return n, errExpansion
	}
	return n, err
}
