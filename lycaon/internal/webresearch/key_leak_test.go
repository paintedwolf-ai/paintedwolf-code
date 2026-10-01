package webresearch

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const sentinelKeySecret = "SECRET_SENTINEL_VAL_DO_NOT_LEAK_12345"

type keyLeakMockTransport struct {
	err error
}

func (m *keyLeakMockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, m.err
}

func TestWebResearchKeyLeakInvariant(t *testing.T) {
	cat, err := LoadCatalog()
	testutil.FailErr(t, "LoadCatalog", err)
	reg := NewRegistry(cat)
	testutil.FailErr(t, "RegisterCatalogProviders", RegisterCatalogProviders(reg))

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := NewConfigStoreAt(cfgPath)

	credPath := filepath.Join(t.TempDir(), "creds.age")
	creds := NewCredentialStoreAt(credPath, cat)

	for _, entry := range cat.Entries() {
		if entry.CredentialSlot != "" {
			testutil.FailErr(t, "set cred", creds.Set(entry.CredentialSlot, sentinelKeySecret))
		}
		if entry.OptionalCredentialSlot != "" {
			testutil.FailErr(t, "set opt cred", creds.Set(entry.OptionalCredentialSlot, sentinelKeySecret))
		}
		configMap := map[string]string{}
		if entry.DefaultEndpoint != "" {
			configMap["endpoint"] = entry.DefaultEndpoint
		} else {
			configMap["endpoint"] = "https://mock.unreachable.endpoint"
		}
		for _, field := range entry.ExtraFields {
			configMap[field.Name] = "test-extra-" + field.Name
		}
		testutil.FailErr(t, "set config", cfg.SetProviderConfig(entry.ID, configMap))
	}

	rt := Runtime{
		Catalog:  cat,
		Registry: reg,
		Config:   cfg,
		Creds:    creds,
	}

	failureCases := []struct {
		name string
		err  error
	}{
		{
			name: "connection_refused",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
		},
		{
			name: "dns_failure",
			err:  &net.DNSError{Err: "no such host", Name: "mock.unreachable.endpoint"},
		},
		{
			name: "timeout",
			err:  context.DeadlineExceeded,
		},
		{
			name: "tls_record_error",
			err:  tls.RecordHeaderError{Msg: "tls record error"},
		},
		{
			name: "dial_failure",
			err:  errors.New("dial tcp 127.0.0.1:0: connect: connection refused"),
		},
	}

	for _, fc := range failureCases {
		t.Run(fc.name, func(t *testing.T) {
			client := &http.Client{
				Transport: &keyLeakMockTransport{err: fc.err},
			}
			injectProviderHTTPClient(t, client)

			for _, entry := range cat.Entries() {
				if isDirectProvider(entry.ID) {
					continue
				}
				t.Run(entry.ID, func(t *testing.T) {
					// 1. SearchProvider.Search
					p := reg.Get(entry.ID)
					if p != nil {
						settings := DefaultSettings(creds, cfg, cat)
						settings.Keys[entry.ID] = sentinelKeySecret
						outcome := p.Search(t.Context(), settings, "test query", 5)
						assertNoSecretLeak(t, "providerOutcome.detail", outcome.detail)
						assertNoSecretLeak(t, "providerOutcome.reason", outcome.reason)
					}

					// 2. High-level Search
					settings := DefaultSettings(creds, cfg, cat)
					settings.Keys[entry.ID] = sentinelKeySecret
					searchRes := Search(t.Context(), SearchOptions{
						Query:           "test query",
						Limit:           5,
						Provider:        entry.ID,
						Settings:        settings,
						Registry:        reg,
						SkipResultCache: true,
					})
					assertNoSecretLeak(t, "searchRes.Error", searchRes.Error)
					for _, skip := range searchRes.ProvidersSkipped {
						assertNoSecretLeak(t, "ProvidersSkipped.Reason", skip.Reason)
					}
					for _, hit := range searchRes.Results {
						assertNoSecretLeak(t, "hit.Title", hit.Title)
						assertNoSecretLeak(t, "hit.Snippet", hit.Snippet)
						assertNoSecretLeak(t, "hit.URL", hit.URL)
					}

					// 3. Runtime.TestProvider
					testRes, err := rt.TestProvider(t.Context(), entry.ID, nil)
					if err != nil {
						assertNoSecretLeak(t, "TestProvider err", err.Error())
					}
					assertNoSecretLeak(t, "testRes.Error", testRes.Error)
				})
			}
		})
	}
}

func assertNoSecretLeak(t *testing.T, field, value string) {
	t.Helper()
	if strings.Contains(strings.ToLower(value), strings.ToLower(sentinelKeySecret)) {
		t.Fatalf("%s leaked sentinel key! Value: %q", field, value)
	}
}
