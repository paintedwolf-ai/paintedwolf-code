package db

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStartupIntegrityProgress(t *testing.T) {
	for _, tc := range []struct {
		name         string
		clean, audit bool
		want         []IntegrityPhase
	}{
		{name: "clean shutdown", clean: true},
		{name: "unclean shutdown", want: []IntegrityPhase{IntegrityStructure}},
		{name: "unfinished audit", clean: true, audit: true, want: []IntegrityPhase{IntegrityIndexes, IntegrityReferences}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			store, err := Open(path)
			testutil.FailErr(t, "create store", err)
			testutil.FailErr(t, "close store", store.Close())
			writer, err := openWriter(t.Context(), path, StoreOptions{})
			testutil.FailErr(t, "open fixture writer", err)
			testutil.FailErr(t, "set shutdown state", markShutdownState(t.Context(), writer, tc.clean))
			if tc.audit {
				testutil.FailErr(t, "record unfinished audit", prepareAuditState(t.Context(), writer, true))
			}
			testutil.FailErr(t, "close fixture writer", writer.Close())
			var got []IntegrityPhase
			store, err = OpenWithOptions(t.Context(), path, UpgradeHooks{IntegrityProgress: func(phase IntegrityPhase) {
				got = append(got, phase)
			}}, StoreOptions{})
			testutil.FailErr(t, "reopen store", err)
			t.Cleanup(func() { _ = store.Close() })
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("startup checks = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCancelledStartupIntegrityDoesNotReportCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	store, err := Open(path)
	testutil.FailErr(t, "create store", err)
	testutil.FailErr(t, "mark unclean", markShutdownState(t.Context(), store.writer, false))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var got []IntegrityPhase
	err = prepareIntegrity(ctx, store.reader, func(phase IntegrityPhase) {
		got = append(got, phase)
		cancel()
	})
	t.Cleanup(func() { _ = store.Close() })
	if err == nil || !reflect.DeepEqual(got, []IntegrityPhase{IntegrityStructure}) {
		t.Fatalf("cancelled check = %v, phases %v", err, got)
	}
}
