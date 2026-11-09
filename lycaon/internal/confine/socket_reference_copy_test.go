package confine

import (
	"errors"
	"testing"
)

func TestSocketRecoveryReferencesUseTheLaunchGrammar(t *testing.T) {
	for _, path := range []string{"/tmp/service.sock", "/tmp/service with % and {braces}.sock"} {
		plain, url := SocketEndpointReferences(path)
		for _, ref := range []string{plain, url} {
			_, _, err := ResolveSocketArguments(nil, []string{ref}, nil)
			var rejected *SocketResolveError
			if !errors.As(err, &rejected) || rejected.Code != SocketResolveChanged || rejected.Path != path {
				t.Fatalf("reference %q failed grammar round trip: %v", ref, err)
			}
		}
	}
	if plain, url := SocketEndpointReferences("relative"); plain != "" || url != "" {
		t.Fatal("invalid path acquired reference syntax")
	}
}
