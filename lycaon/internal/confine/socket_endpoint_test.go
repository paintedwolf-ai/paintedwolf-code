//go:build darwin || linux

package confine

import (
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSocketReferencesDoNotRewriteOrdinaryArgumentsOrEnvironment(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pw-ref-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "create socket reference fixture", err)
	defer os.RemoveAll(root)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(root, "state"))
	path := filepath.Join(root, "service %}")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen for explicit socket reference", err)
	defer listener.Close()
	owner, err := socketref.Capture(path)
	testutil.FailErr(t, "capture explicit socket reference", err)
	defer owner.Close()
	grant := SocketGrant{ApprovedPath: path, ResolvedPath: path, Binding: owner.Binding()}
	boundary := &Confinement{SocketGrants: []SocketGrant{grant}, socketEndpoints: map[string]string{socketEndpointKey(grant.Binding, "unix"): "/bound/endpoint", socketEndpointKey(grant.Binding, "unixgram"): "/bound/datagrams"}}
	reference := socketReferencePrefix + url.PathEscape(path) + "}}"
	args := []string{"/bin/rm", path, reference, "--socket=" + path}
	env := []string{"PATH=/usr/bin:/bin", "ORIGINAL=" + path, "SERVICE=" + reference}
	originalArgs, originalEnv := append([]string(nil), args...), append([]string(nil), env...)
	resolved, environment, err := ResolveSocketArguments(boundary, args, env)
	testutil.FailErr(t, "resolve explicitly bound endpoint", err)
	if !reflect.DeepEqual(resolved, []string{"/bin/rm", path, "/bound/endpoint", "--socket=" + path}) {
		t.Fatalf("argument meanings changed: %#v", resolved)
	}
	if !reflect.DeepEqual(environment, []string{"PATH=/usr/bin:/bin", "ORIGINAL=" + path, "SERVICE=/bound/endpoint"}) {
		t.Fatalf("environment meanings changed: %#v", environment)
	}
	if !reflect.DeepEqual(args, originalArgs) || !reflect.DeepEqual(env, originalEnv) {
		t.Fatal("endpoint resolution mutated caller inputs")
	}
	owner.Close()
	if _, _, err := ResolveSocketArguments(boundary, []string{reference}, nil); err == nil {
		t.Fatal("released authority supplied an endpoint")
	}
}

func TestSocketReferencesRequireExactLiveReviewedAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		boundary    *Confinement
		code        string
	}{
		{"missing authority", "{{paintedwolf-socket:/tmp/service}}", nil, string(SocketResolveChanged)},
		{"embedded source", "s.connect('{{paintedwolf-socket:/tmp/service}}')", nil, string(SocketResolveInvalid)},
		{"invalid encoding", "{{paintedwolf-socket:/tmp/%xx}}", nil, string(SocketResolveInvalid)},
		{"relative target", "{{paintedwolf-socket:relative}}", nil, string(SocketResolveInvalid)},
		{"NUL target", "{{paintedwolf-socket:/tmp/%00}}", nil, string(SocketResolveInvalid)},
		{"unknown instance", "{{paintedwolf-socket:/tmp/service}}", &Confinement{SocketGrants: []SocketGrant{{ApprovedPath: "/tmp/service", ResolvedPath: "/tmp/service", Binding: socketref.Binding{Enforcement: socketref.RetainedObject, ID: "forged"}}}, socketEndpoints: map[string]string{"retained_object\x00forged": "/bound/endpoint"}}, string(SocketResolveChanged)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ResolveSocketArguments(tc.boundary, []string{tc.value}, nil)
			var refused *SocketResolveError
			if !errors.As(err, &refused) || string(refused.Code) != tc.code {
				t.Fatalf("endpoint refusal=%v, want code=%s", err, tc.code)
			}
		})
	}
}
