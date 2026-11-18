package modelstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/httpclient"
)

type ModelPin struct {
	Model, Device, Revision, ManifestSHA256 string
	TotalBytes                              int64
}

func (s *Store) ImportFromBucket(ctx context.Context, pin ModelPin, progress func(done, total int64)) error {
	client := httpclient.Download(egressclass.LocalModelDownload, 4*time.Hour)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer client.CloseIdleConnections()
	origin := egressclass.RequireSingleFixedEndpoint(egressclass.LocalModelDownload, egressclass.HTTPDownload)
	return s.download(ctx, origin, client, pin, progress)
}

func (s *Store) download(ctx context.Context, origin string, client *http.Client, pin ModelPin, progress func(int64, int64)) (resultErr error) {
	if err := validateIdentity(Manifest{Model: pin.Model, Device: pin.Device, Revision: pin.Revision}); err != nil {
		return err
	}
	if !digestShape.MatchString(pin.ManifestSHA256) || pin.TotalBytes <= 0 {
		return errors.New("modelstore: incomplete model download pin")
	}
	lock, err := s.provisioningLock()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	base := fmt.Sprintf("%s/models/%s/%s/%s/", origin, pin.Model, pin.Device, pin.Revision)
	signed, err := fetchManifest(ctx, client, base+"manifest.json", pin)
	if err != nil {
		return err
	}
	transfer := transferProgress{total: pin.TotalBytes, report: progress}
	return s.install(ctx, signed, true, nil, func(a Artifact) error {
		if s.reusableArtifact(ctx, a) {
			transfer.add(a.Bytes)
			return nil
		}
		return s.downloadArtifact(ctx, client, base+a.Name, a, &transfer)
	})
}

func fetchManifest(ctx context.Context, client *http.Client, url string, pin ModelPin) (signed SignedManifest, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return signed, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return signed, err
	}
	defer func() { resultErr = errors.Join(resultErr, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return signed, fmt.Errorf("modelstore: manifest HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBundleManifestBytes+1))
	if err != nil {
		return signed, err
	}
	if len(raw) > maxBundleManifestBytes || fmt.Sprintf("%x", sha256.Sum256(raw)) != pin.ManifestSHA256 {
		return signed, errors.New("modelstore: manifest differs from download pin")
	}
	signed, err = decodeBundleManifest(bytes.NewReader(raw))
	if err != nil {
		return signed, err
	}
	m := signed.Manifest
	if m.Model != pin.Model || m.Device != pin.Device || m.Revision != pin.Revision || manifestSize(m) != pin.TotalBytes {
		return signed, errors.New("modelstore: manifest does not match pinned model")
	}
	return signed, nil
}

func manifestSize(m Manifest) int64 {
	total := m.Artifact.Bytes
	for _, a := range m.Files {
		total += a.Bytes
	}
	return total
}

type transferProgress struct {
	done, total int64
	report      func(int64, int64)
}

func (t *transferProgress) add(n int64) {
	t.done += n
	if t.report != nil {
		t.report(t.done, t.total)
	}
}

func (t *transferProgress) Write(p []byte) (int, error) { t.add(int64(len(p))); return len(p), nil }
