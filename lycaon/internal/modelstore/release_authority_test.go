package modelstore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReleaseAuthorityVerifiesOnlyPinnedKeys(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate fixture release key", err)
	document := map[string]any{"format": 1, "keys": []any{map[string]any{"id": "fixture", "public_key": base64.StdEncoding.EncodeToString(public)}}}
	raw, err := json.Marshal(document)
	testutil.FailErr(t, "encode release keys", err)
	authority, err := DecodeAuthority(raw)
	testutil.FailErr(t, "decode release keys", err)
	manifest := Manifest{Model: "fixture", Revision: "r1", Device: "mlx", Artifact: Artifact{Digest: strings.Repeat("a", 64), Bytes: 1, Format: "safetensors"}}
	message, err := SigningBytes(manifest)
	testutil.FailErr(t, "encode fixture manifest", err)
	signed := SignedManifest{Manifest: manifest, KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, message))}
	testutil.FailErr(t, "verify pinned manifest", authority.Verify(signed))
	signed.KeyID = "untrusted"
	if err := authority.Verify(signed); err == nil {
		t.Fatal("accepted an unpinned signing key")
	}
	for _, invalid := range [][]byte{
		nil, []byte(`{}`), []byte(`{"format":2,"keys":[]}`),
		append(append([]byte(nil), raw...), []byte(` {}`)...),
		[]byte(strings.Repeat(" ", MaxAuthorityBytes+1)),
		[]byte(strings.Replace(string(raw), `"format":1`, `"format":1,"extra":true`, 1)),
	} {
		if _, err := DecodeAuthority(invalid); err == nil {
			t.Fatalf("accepted invalid authority: %q", invalid[:min(80, len(invalid))])
		}
	}
	document["keys"] = append(document["keys"].([]any), document["keys"].([]any)[0])
	raw, err = json.Marshal(document)
	testutil.FailErr(t, "encode duplicate release key", err)
	if _, err := DecodeAuthority(raw); err == nil {
		t.Fatal("accepted duplicate signing identities")
	}
}
