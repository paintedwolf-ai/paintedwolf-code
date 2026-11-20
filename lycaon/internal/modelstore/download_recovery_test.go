package modelstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

type bucketTransport func(*http.Request) (*http.Response, error)

func (f bucketTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestInterruptedPayloadResumesAfterRestart(t *testing.T) {
	for _, mode := range []string{"stall", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, _, signed := bundleFixture(t)
			pin, raw := pinnedBundle(t, signed)
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				transport := bucketTransport(func(req *http.Request) (*http.Response, error) {
					resp := &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Header: make(http.Header)}
					switch {
					case strings.HasSuffix(req.URL.Path, "/manifest.json"):
						resp.Body = io.NopCloser(bytes.NewReader(raw))
					case req.Header.Get("Range") == "bytes=3-":
						resp.StatusCode = http.StatusPartialContent
						resp.Header.Set("Content-Range", "bytes 3-6/7")
						left := strings.NewReader("ghts")
						resp.Body = io.NopCloser(payloadReaderFunc(func(p []byte) (int, error) {
							time.Sleep(payloadReadTimeout / 2)
							return left.Read(p[:1])
						}))
					case req.Header.Get("Range") == "":
						stall := payloadReaderFunc(func([]byte) (int, error) {
							if mode == "cancel" {
								cancel()
							}
							<-req.Context().Done()
							return 0, req.Context().Err()
						})
						resp.Body = io.NopCloser(io.MultiReader(strings.NewReader("wei"), stall))
					default:
						t.Fatalf("unexpected range %q", req.Header.Get("Range"))
					}
					return resp, nil
				})
				src := modelBucket{base: "https://example.test", client: &http.Client{Transport: transport}}
				err := s.download(ctx, src, pin, nil)
				want := errPayloadStalled
				if mode == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("interrupted download: %v", err)
				}
				fresh := New(s.Root(), s.signer)
				testutil.FailErr(t, "resume interrupted transfer", fresh.download(t.Context(), src, pin, nil))
				if fresh.Status(pin.Model, pin.Device).State != StateReady {
					t.Fatal("resumed revision unavailable")
				}
			})
		})
	}
}

func TestCompletePartialNeedsOnlyReserve(t *testing.T) {
	s, _, signed := bundleFixture(t)
	m := signed.Manifest
	root, err := s.openRoot()
	testutil.FailErr(t, "open store", err)
	defer root.Close()
	testutil.FailErr(t, "create partial directory", root.Mkdir("partials", 0700))
	testutil.FailErr(t, "write completed partial", root.WriteFile(filepath.Join("partials", m.Artifact.Digest), []byte("weights"), 0600))
	s.SetFreeBytesFunc(func(string) (int64, error) { return installReserve, nil })
	testutil.FailErr(t, "admit completed partial", s.admitInstall(t.Context(), m, true, nil))
}

func TestExplicitReinstallRepairsLeasedRevisionAfterRelease(t *testing.T) {
	s, dir, signed := bundleFixture(t)
	m := signed.Manifest
	install := func() error { return s.ImportFromBundlePath(t.Context(), dir, Slot{m.Model, m.Device}, nil) }
	testutil.FailErr(t, "install signed model", install())
	lease, err := s.AcquireLease(m.Model, m.Device, "runtime", 1)
	testutil.FailErr(t, "acquire installed model", err)
	defer lease.Release()
	path := filepath.Join(lease.Dir, m.Artifact.Name)
	testutil.FailErr(t, "remove revision link", os.Remove(path))
	if err := install(); !errors.Is(err, ErrRevisionInUse) {
		t.Fatalf("reinstall while leased: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("leased tree changed: %v", err)
	}
	lease.Release()
	testutil.FailErr(t, "reinstall signed model", install())
	repaired, err := s.AcquireLease(m.Model, m.Device, "runtime", 2)
	testutil.FailErr(t, "acquire rebuilt revision", err)
	defer repaired.Release()
	testutil.FailErr(t, "audit repaired model", repaired.Audit(t.Context()))
}
