package browser

import (
	"errors"
	"github.com/lycaon/lycaon/internal/browserengine"
	"strings"
	"testing"
)

func TestValidateMarkupRejectsScript(t *testing.T) {
	err := ValidateMarkup(`<html><script>alert(1)</script></html>`, "html")
	if err == nil {
		t.Fatal("expected reject for script tag")
	}
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "RENDER_MARKUP_FORBIDDEN" {
		t.Fatalf("got %#v want RENDER_MARKUP_FORBIDDEN", err)
	}
}

func TestValidateMarkupRejectsExternalHref(t *testing.T) {
	err := ValidateMarkup(`<svg><image href="https://example.com/x.png"/></svg>`, "svg")
	if err == nil {
		t.Fatal("expected reject for external href")
	}
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "RENDER_MARKUP_FORBIDDEN" {
		t.Fatalf("got %#v want RENDER_MARKUP_FORBIDDEN", err)
	}
}

func TestValidateMarkupAcceptsInlineSVG(t *testing.T) {
	markup := `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="100"><rect width="100" height="100" fill="blue"/></svg>`
	if err := ValidateMarkup(markup, "svg"); err != nil {
		t.Fatalf("inline svg: %v", err)
	}
}

func TestValidateMarkupAllowsHermeticAssetRefs(t *testing.T) {
	cases := []string{
		`<img src="http://lycaon.asset/assets/logo.png" alt="logo">`,
		`<div style="background-image:url(http://lycaon.asset/assets/bg.webp)">x</div>`,
		`<link rel="stylesheet" href="http://lycaon.asset/styles/app.css">`,
		`<link href="http://lycaon.asset/styles/app.css" rel="stylesheet" type="text/css" media="screen">`,
		`<style>@import url(http://lycaon.asset/styles/base.css);</style>`,
		`<style>@import "http://lycaon.asset/styles/base.css";</style>`,
		`<svg><image href="http://lycaon.asset/docs/hero.svg"/></svg>`,
	}
	for _, markup := range cases {
		if err := ValidateMarkup(markup, "html"); err != nil {
			t.Fatalf("hermetic asset ref rejected: %s → %v", markup, err)
		}
	}
}

func TestValidateMarkupStillRejectsNonAssetNetwork(t *testing.T) {
	cases := []string{
		`<img src="https://cdn.example/logo.png">`,
		`<img src="http://lycaon.evil/logo.png">`,
		`<link rel="stylesheet" href="https://cdn.example/app.css">`,
		`<link rel="stylesheet" href="http://lycaon.asset/app.css" onload="alert(1)">`,
		`<style>@import url(https://cdn.example/base.css);</style>`,
		`<style>@import "https://cdn.example/base.css";</style>`,
		`<style>@import "local.css";</style>`,
		`<div style="background:url(https://cdn.example/bg.png)">x</div>`,
		`<script src="http://lycaon.asset/evil.js"></script>`,
		`<iframe src="http://lycaon.asset/page.html"></iframe>`,
	}
	for _, markup := range cases {
		err := ValidateMarkup(markup, "html")
		if err == nil {
			t.Fatalf("expected reject: %s", markup)
		}
		rej := &browserengine.RejectError{}
		if !errors.As(err, &rej) || rej.Code != "RENDER_MARKUP_FORBIDDEN" {
			t.Fatalf("got %#v want RENDER_MARKUP_FORBIDDEN for %s", err, markup)
		}
	}
}

func TestValidateMarkupRejectsOversized(t *testing.T) {
	err := ValidateMarkup(strings.Repeat("a", MaxMarkupBytes+1), "html")
	if err == nil {
		t.Fatal("expected oversize reject")
	}
	rej := &browserengine.RejectError{}
	ok := errors.As(err, &rej)
	if !ok || rej.Code != "RENDER_MARKUP_OVERSIZED" {
		t.Fatalf("got %#v want RENDER_MARKUP_OVERSIZED", err)
	}
}
