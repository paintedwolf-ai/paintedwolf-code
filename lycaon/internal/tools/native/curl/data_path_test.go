package curl

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestCurlPreservesSecretShapedResponseBytes(t *testing.T) {
	body := "aws=AKIAQYJK5TXV4NZR7SGB\ngithub=ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3\n"
	server := fixedServer(t, func(w http.ResponseWriter, r *http.Request) {
		stamp(w, "text/plain")
		w.Header().Set("X-Token", "AKIAQYJK5TXV4NZR7SGB")
		http.SetCookie(w, &http.Cookie{Name: "token", Value: "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3", Path: "/"})
		_, _ = io.WriteString(w, body)
	})
	h := newHarness(t)
	for _, file := range []bool{false, true} {
		args := []string{"-s"}
		if file {
			args = append(args, "-o", "body.txt", "-D", "headers.txt", "-c", "cookies.txt")
		}
		r := h.curl(t.Context(), append(args, server.URL)...)
		actual := r.stdout
		if file {
			actual = h.read("body.txt")
		}
		sum := sha256.Sum256([]byte(body))
		digest := hex.EncodeToString(sum[:])
		if r.err != nil || actual != body || r.transfer.ReceivedSHA256 != digest || r.transfer.EmittedSHA256 != digest || r.transfer.EmittedBytes != int64(len(body)) {
			t.Fatalf("response changed (file=%v): %q %+v %v", file, actual, r.transfer, r.err)
		}
		if file && (!contains(h.read("headers.txt"), "X-Token: AKIAQYJK5TXV4NZR7SGB") || !contains(h.read("headers.txt"), "Set-Cookie: token=ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3") || !contains(h.read("cookies.txt"), "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3")) {
			t.Fatal("response header or cookie values changed")
		}
	}
}

func TestCurlFilePublicationRequiresBothNetworkAndMutation(t *testing.T) {
	h := newHarness(t)
	server := fixedServer(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "body") })
	r := h.curl(t.Context(), "-s", "-o", "downloaded", server.URL)
	if r.err != nil {
		t.Fatal(r.err)
	}
	mutationOnly, _ := toolcontract.NewCommandEffects(toolcontract.CommandMutation)
	networkOnly, _ := toolcontract.NewCommandEffects(toolcontract.CommandNetworkEgress)
	both, _ := toolcontract.NewCommandEffects(toolcontract.CommandMutation, toolcontract.CommandNetworkEgress)
	var required toolcontract.CommandEffects
	for _, effect := range h.owners.spec.Effects {
		if effect.Mode == r.mode {
			var err error
			required, err = effect.CommandEffects(nil)
			testutil.FailErr(t, "resolve command effect classes", err)
		}
	}
	if required == 0 || mutationOnly.Allows(required) || networkOnly.Allows(required) || !both.Allows(required) {
		t.Fatalf("file request admitted without both authorities: mode=%q effects=%v", r.mode, required.Classes())
	}
}
