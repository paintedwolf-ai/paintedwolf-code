package browser

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVideoDecodeReportsMediaErrorsAtEveryWait(t *testing.T) {
	pool := drivePool(t)
	page, err := pool.NewPage(t.Context(), 640, 360)
	testutil.FailErr(t, "create decoder page", err)
	defer func() { _ = page.Close() }()
	for _, phase := range []string{"before_metadata", "metadata", "duration", "seek"} {
		t.Run(phase, func(t *testing.T) {
			result, err := page.Context(t.Context()).Eval(`async (decode, phase) => {
  const video = new EventTarget();
  Object.assign(video, {readyState: phase === "before_metadata" || phase === "metadata" ? 0 : 2,
    duration: phase === "duration" ? Infinity : 1, videoWidth: 320, videoHeight: 180});
  const fail = () => { video.error = {code: 4}; video.dispatchEvent(new Event("error")); };
  if (phase === "before_metadata") fail();
  if (phase === "metadata") queueMicrotask(fail);
  Object.defineProperty(video, "currentTime", {set() { video.seeking = true; queueMicrotask(fail); }});
  const getElementById = document.getElementById;
  document.getElementById = () => video;
  try { return await (0, eval)("(" + decode + ")")([500], 0, {}, null, 160, 0.86); }
  finally { document.getElementById = getElementById; }
}`, videoDecodeScript, phase)
			testutil.FailErr(t, "decode media error", err)
			if result.Value.Get("error").Str() != "media_error" || result.Value.Get("code").Int() != 4 {
				t.Fatalf("media error result = %s", result.Value.JSON("", ""))
			}
		})
	}
}
