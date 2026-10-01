package sourceview

import (
	"bytes"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVueOnlyProjectsTopLevelScripts(t *testing.T) {
	source := []byte(`<template><script>eval(location.hash)</script></template><custom><script>eval(location.search)</script></custom><script>JSON.parse(location.hash)</script>`)
	projected, _, err := Scripts(t.Context(), source, true)
	testutil.FailErr(t, "project component script", err)
	if len(projected) != 1 || bytes.Contains(projected[0].Source, []byte("eval")) || !bytes.Contains(projected[0].Source, []byte("JSON.parse")) {
		t.Fatalf("component scopes = %+v", projected)
	}
}

func TestVueRejectsConflictingScriptBlocks(t *testing.T) {
	for _, source := range []string{
		`<script>const a = 1;</script><script>const b = 2;</script>`,
		`<script setup>const a = 1;</script><script setup>const b = 2;</script>`,
		`<script lang="ts">const a = 1;</script><script setup>const b = 2;</script>`,
		`<script src="./module.js"></script><script setup>const b = 2;</script>`,
	} {
		t.Run(source, func(t *testing.T) {
			_, _, err := Scripts(t.Context(), []byte(source), true)
			var limitation *Limitation
			if !errors.As(err, &limitation) || limitation.Construct != "vue_sfc" {
				t.Fatalf("conflicting blocks: %v", err)
			}
		})
	}
}

func TestVuePreservesCompilerAttributeNames(t *testing.T) {
	for _, source := range []string{
		`<script LANG="coffee">eval(location.hash)</script>`,
		`<script lang="&#116;s">eval(location.hash)</script>`,
		`<script>  </script><script>eval(location.hash)</script>`,
	} {
		projected, _, err := Scripts(t.Context(), []byte(source), true)
		testutil.FailErr(t, "project exact component attributes", err)
		if len(projected) != 1 {
			t.Fatalf("projected scripts = %d", len(projected))
		}
	}
}
