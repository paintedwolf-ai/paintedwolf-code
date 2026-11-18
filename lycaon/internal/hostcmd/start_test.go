package hostcmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPrepareStartChecksAuthorityAndReachedExecutables(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix executable fixtures")
	}
	dir := t.TempDir()
	testutil.FailErr(t, "write executable", os.WriteFile(filepath.Join(dir, "stage-tool"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	authorityChanged := errors.New("authority changed")
	for _, tc := range []struct {
		name   string
		stages []exec.Stage
		refuse error
		cancel bool
		ready  bool
	}{
		{name: "cancelled", cancel: true},
		{name: "no stages"},
		{name: "authority changed", refuse: authorityChanged, stages: []exec.Stage{{Name: "/bin/true"}}},
		{name: "missing pipeline stage", stages: []exec.Stage{{Name: "/bin/true"}, {Name: "./missing"}}},
		{name: "sequenced stage not reached", stages: []exec.Stage{{Name: "/bin/false"}, {Name: "./missing", Connector: argv.ConnectorAnd}}, ready: true},
		{name: "effective search path", stages: []exec.Stage{{Name: "stage-tool"}}, ready: true},
		{name: "reserved stage environment", stages: []exec.Stage{{Name: "stage-tool", Env: map[string]string{"PATH": dir}}}, refuse: exec.ErrBlockedEnvKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			var called bool
			want := SourceIdentity{Revision: "revision", RootDigest: "root"}
			req := Request{
				ProjectDir: dir, Stages: tc.stages, Launch: exec.HostLaunch("fixture"),
				BeforeStart: func(context.Context) (SourceIdentity, error) {
					called = true
					return want, nil
				},
			}
			req.PathExtra = []string{dir}
			if tc.refuse == authorityChanged {
				req.Launch.CurrentAuthority = func(context.Context) error { return authorityChanged }
			}
			got, err := req.PrepareStart(ctx)
			if tc.ready {
				if !called || got != want || err != nil {
					t.Fatalf("preparation called=%v, source=%+v, err=%v", called, got, err)
				}
			} else if called || err == nil || tc.refuse != nil && !errors.Is(err, tc.refuse) {
				t.Fatalf("preparation called=%v, err=%v", called, err)
			}
		})
	}
}
