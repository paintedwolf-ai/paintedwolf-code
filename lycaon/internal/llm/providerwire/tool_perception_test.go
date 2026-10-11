package providerwire

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func perceptionPNG(t *testing.T) []byte {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	testutil.FailErr(t, "decode fixture png", err)
	return png
}

func storedVisualMessage(callID, artifactID string) api.Message {
	return api.Message{
		Role:    api.MessageRoleTool,
		Content: `{"captured":true}`,
		ToolResult: &api.ToolResult{
			ToolCallID: callID,
			Tool:       "capture_page",
			Content:    `{"captured":true}`,
			Visual: &api.VisualArtifact{
				ID: artifactID, Mime: "image/png", StoreRef: true, Perceive: true, Width: 1, Height: 1,
			},
		},
	}
}

func withVisualResolver(t *testing.T, fn VisualBytesResolver) {
	t.Helper()
	SetVisualBytesResolver(fn)
	t.Cleanup(func() { SetVisualBytesResolver(nil) })
}

func TestStoredToolImageIsAttachedWithItsBytes(t *testing.T) {
	png := perceptionPNG(t)
	withVisualResolver(t, func(sessionID, artifactID string) ([]byte, string, bool) {
		if sessionID != "sess-1" || artifactID != "art-1" {
			return nil, "", false
		}
		return png, "image/png", true
	})

	out := PrepareMessagesForVision([]api.Message{storedVisualMessage("call-1", "art-1")}, true, "sess-1")

	image, ok := ToolResultImage(out[0])
	if !ok {
		t.Fatalf("stored capture was not attached: %+v", out[0].ToolResult.Visual)
	}
	if image.Mime != "image/png" || image.Base64 != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("image = %+v", image)
	}
	if !strings.Contains(out[0].Content, "⟦D:host:perception⟧") || !strings.Contains(out[0].Content, `"image":"attached"`) {
		t.Fatalf("content lacks the attached fact: %q", out[0].Content)
	}
}

func TestToolImageDetachReasonsAreStated(t *testing.T) {
	cases := map[string]struct {
		vision   bool
		resolver VisualBytesResolver
		want     PerceptionOutcome
	}{
		"no vision input": {
			vision: false,
			want:   PerceptionNoVisionInput,
		},
		"bytes unavailable": {
			vision:   true,
			resolver: func(string, string) ([]byte, string, bool) { return nil, "", false },
			want:     PerceptionBytesUnavailable,
		},
		"not an image": {
			vision:   true,
			resolver: func(string, string) ([]byte, string, bool) { return []byte("not a png"), "image/png", true },
			want:     PerceptionNotEncodable,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			withVisualResolver(t, tc.resolver)
			out := PrepareMessagesForVision([]api.Message{storedVisualMessage("call-1", "art-1")}, tc.vision, "sess-1")
			if _, ok := ToolResultImage(out[0]); ok {
				t.Fatal("image attached despite the failure")
			}
			want := fmt.Sprintf(`"image":"not_attached","reason":%q,"artifact_id":"art-1"`, tc.want)
			if !strings.Contains(out[0].Content, want) {
				t.Fatalf("content = %q, want fact %s", out[0].Content, want)
			}
			if out[0].ToolResult.Visual.Perceive {
				t.Fatal("a detached visual must not stay perceive-flagged")
			}
		})
	}
}

func TestPerceptionWindowDetachesOldestImagesInBatches(t *testing.T) {
	png := perceptionPNG(t)
	withVisualResolver(t, func(string, string) ([]byte, string, bool) { return png, "image/png", true })
	SetPerceptionWindow(PerceptionWindow{MaxToolImages: 4, DropBatch: 2})
	t.Cleanup(func() { SetPerceptionWindow(DefaultPerceptionWindow) })

	var msgs []api.Message
	for i := range 5 {
		msgs = append(msgs, storedVisualMessage(fmt.Sprintf("call-%d", i), fmt.Sprintf("art-%d", i)))
	}
	out := PrepareMessagesForVision(msgs, true, "sess-1")

	// Five images over a window of four drop a whole batch of two.
	for i, m := range out {
		_, attached := ToolResultImage(m)
		if want := i >= 2; attached != want {
			t.Fatalf("image %d attached = %v, want %v", i, attached, want)
		}
		if !attached && !strings.Contains(m.Content, `"reason":"outside_window"`) {
			t.Fatalf("image %d content = %q", i, m.Content)
		}
	}
}

func TestPerceptionWindowDropIsStableWithinABatch(t *testing.T) {
	w := PerceptionWindow{MaxToolImages: 8, DropBatch: 4}
	for total, want := range map[int]int{0: 0, 8: 0, 9: 4, 12: 4, 13: 8, 16: 8, 17: 12} {
		if got := w.Dropped(total); got != want {
			t.Fatalf("Dropped(%d) = %d, want %d", total, got, want)
		}
	}
}

func TestPrepareLeavesMessagesWithoutImagesUntouched(t *testing.T) {
	msgs := []api.Message{{Role: api.MessageRoleTool, Content: "plain", ToolResult: &api.ToolResult{Content: "plain"}}}
	out := PrepareMessagesForVision(msgs, true, "sess-1")
	if out[0].Content != "plain" {
		t.Fatalf("content = %q", out[0].Content)
	}
}

func TestImageTokenEstimateScalesWithArea(t *testing.T) {
	if got := ImageTokenEstimate(2048, 1152); got < 3000 || got > 3300 {
		t.Fatalf("2048x1152 estimate = %d, want about 3,100", got)
	}
	if ImageTokenEstimate(4096, 2304) != ImageTokenEstimate(2048, 1152) {
		t.Fatal("images over the edge bound must be charged at their downscaled size")
	}
	if got := ImageTokenEstimate(0, 0); got != UnsizedImageTokenEstimate {
		t.Fatalf("unsized estimate = %d", got)
	}
}

func TestVisualResolverReleasePreservesReplacement(t *testing.T) {
	oldCalls, currentCalls := 0, 0
	releaseOld := SetVisualBytesResolver(func(string, string) ([]byte, string, bool) { oldCalls++; return nil, "", false })
	png := perceptionPNG(t)
	releaseCurrent := SetVisualBytesResolver(func(string, string) ([]byte, string, bool) { currentCalls++; return png, "image/png", true })
	t.Cleanup(releaseCurrent)
	releaseOld()
	releaseOld()
	if _, _, ok := UserArtifactWireImage("session", "artifact"); !ok {
		t.Fatal("old owner removed current visual resolver")
	}
	if oldCalls != 0 || currentCalls != 1 {
		t.Fatalf("resolver calls old=%d current=%d", oldCalls, currentCalls)
	}
	releaseCurrent()
	if HasVisualResolver() {
		t.Fatal("released visual resolver remains installed")
	}
	if _, _, ok := UserArtifactWireImage("session", "artifact"); ok {
		t.Fatal("released visual resolver still serves images")
	}
}
