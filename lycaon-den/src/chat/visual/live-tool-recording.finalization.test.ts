import { readFileSync } from "node:fs";
import { afterEach, expect, it, vi } from "vitest";
import { startLiveToolRecording } from "./live-tool-recording.ts";

// Fragmented MP4 from a controlled typing capture.
const rawRecording = readFileSync(
  new URL("./__fixtures__/recording-fragmented.mp4", import.meta.url),
);

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("preserves recorder fragments instead of adding wall time to media duration", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
  const stopTrack = vi.fn();
  const context = { fillStyle: "", fillRect: vi.fn(), drawImage: vi.fn() };
  vi.stubGlobal("document", {
    createElement: () => ({
      width: 0,
      height: 0,
      getContext: () => context,
      captureStream: () => ({ getTracks: () => [{ stop: stopTrack }] }),
    }),
  });
  vi.stubGlobal("window", globalThis);
  vi.stubGlobal("Image", class {
    src = "";
    naturalWidth = 720;
    naturalHeight = 540;
    decode() { return Promise.resolve(); }
    removeAttribute = vi.fn();
  });
  vi.stubGlobal("MediaRecorder", class {
    static isTypeSupported() { return true; }
    state = "inactive";
    ondataavailable?: (event: { data: Blob }) => void;
    onstop?: () => void;
    start() { this.state = "recording"; }
    stop() {
      this.state = "inactive";
      for (const chunk of [rawRecording.subarray(0, 41), rawRecording.subarray(41)]) {
        this.ondataavailable?.({ data: new Blob([Uint8Array.from(chunk)]) });
      }
      this.onstop?.();
    }
  });
  let completedBytes: Uint8Array | undefined;
  let elapsed = 0;
  const recording = startLiveToolRecording(async ({ blob, durationMs }) => {
    completedBytes = new Uint8Array(await blob.arrayBuffer());
    elapsed = durationMs;
  });
  expect(recording).not.toBeNull();
  if (!recording) expect.fail("Recorder did not start");
  recording.pushFrame({ mime: "image/jpeg", jpegB64: "first" });
  await vi.advanceTimersByTimeAsync(0);
  await vi.advanceTimersByTimeAsync(1_000);
  recording.pushFrame({ mime: "image/jpeg", jpegB64: "second" });
  await vi.advanceTimersByTimeAsync(0);
  await vi.advanceTimersByTimeAsync(2_000);
  await recording.stop();

  expect(elapsed).toBe(3_000);
  expect(completedBytes).toEqual(new Uint8Array(rawRecording));
});
