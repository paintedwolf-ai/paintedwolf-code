package bundled

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
)

const maxReleaseManifestBytes = 2 << 20

func releaseClient(client *http.Client) *http.Client {
	var selected http.Client
	if client != nil {
		selected = *client
	} else {
		selected.Timeout = 30 * time.Minute
	}
	previous := selected.CheckRedirect
	selected.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if err := validateReleaseURL(request.URL.String()); err != nil {
			return err
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many release redirects")
		}
		if previous != nil {
			return previous(request, via)
		}
		return nil
	}
	return &selected
}

func openReleaseURL(ctx context.Context, location string, client *http.Client) (*http.Response, error) {
	if err := validateReleaseURL(location); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := releaseClient(client).Do(request)
	if err != nil {
		return nil, fmt.Errorf("request release: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("release request returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func openReleaseFile(path string) (*os.File, error) {
	info, err := candidateRegularFile(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) {
		_ = file.Close()
		return nil, fmt.Errorf("release file changed while opening")
	}
	return file, nil
}

type releaseContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *releaseContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func storeReleaseArchive(ctx context.Context, root string, pin *ReleaseArtifact, options ReleaseFetchOptions) error {
	var reader io.Reader
	if options.Archive != "" {
		file, err := openReleaseFile(options.Archive)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		reader = file
	} else {
		if options.Offline {
			return fmt.Errorf("pinned opengrep release is unavailable in offline cache")
		}
		response, err := openReleaseURL(ctx, pin.URL, options.Client)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		reader = response.Body
		if response.ContentLength >= 0 && response.ContentLength != pin.Bytes {
			return fmt.Errorf("release content length differs from pinned size")
		}
	}
	_, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{Root: root, Rel: pin.SHA256 + ".tar.gz"},
		Source:   io.LimitReader(&releaseContextReader{ctx: ctx, reader: reader}, pin.Bytes+1), Mode: 0o600,
		BeforeCommit: func(_ fseffect.Target, result fseffect.Result) error {
			if result.Bytes != pin.Bytes || result.SHA256 != pin.SHA256 {
				return fmt.Errorf("release archive differs from pinned size or SHA-256")
			}
			return ctx.Err()
		},
	})
	return err
}
