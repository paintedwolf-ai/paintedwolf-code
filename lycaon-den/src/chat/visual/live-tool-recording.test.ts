import { afterEach, describe, expect, it, vi } from "vitest";
import type { LivePreviewSnapshot } from "./preview-store.ts";
import {
  LIVE_TOOL_RECORDING_MIME,
  LiveToolRecordingEpisodeEngine,
  preferredLiveToolRecordingMime,
  type LiveToolRecording,
} from "./live-tool-recording.ts";

function snapshot(
  overrides: Partial<LivePreviewSnapshot> = {},
): LivePreviewSnapshot {
  return {
    sessionId: "session-1",
    holderSessionId: "session-1",
    pageId: "page-1",
    assistantMessageId: "assistant-1",
    toolCallId: "call-1",
    live: true,
    seq: 2,
    revision: 1,
    frameSeq: 2,
    jpegB64: "frame-one",
    mime: "image/jpeg",
    ...overrides,
  };
}

describe("LiveToolRecordingEpisodeEngine", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("prefers an MP4 encoder supported by the desktop player", () => {
    vi.stubGlobal("MediaRecorder", {
      isTypeSupported: vi.fn(
        (mime: string) => mime === "video/mp4;codecs=avc1.42E01E",
      ),
    });

    expect(preferredLiveToolRecordingMime()).toBe(LIVE_TOOL_RECORDING_MIME);
    expect(LIVE_TOOL_RECORDING_MIME).toBe("video/mp4;codecs=avc1.42E01E");
  });

  it("auto-samples the canvas faster than preview draws", async () => {
    const {
      LIVE_TOOL_RECORDING_CAPTURE_FPS,
      LIVE_TOOL_RECORDING_FPS,
    } = await import("./live-tool-recording.ts");
    expect(LIVE_TOOL_RECORDING_CAPTURE_FPS).toBeGreaterThan(
      LIVE_TOOL_RECORDING_FPS,
    );
  });

  it("requires motion before a recording completes or presents", async () => {
    const {
      LIVE_TOOL_RECORDING_FPS,
      LIVE_TOOL_RECORDING_MIN_DURATION_MS,
      LIVE_TOOL_RECORDING_MIN_FRAMES,
    } = await import("./live-tool-recording.ts");
    expect(LIVE_TOOL_RECORDING_MIN_FRAMES).toBeGreaterThanOrEqual(2);
    // Two drawn frames span at least one interval, so every upload passes
    // the reader's duration gate.
    expect(LIVE_TOOL_RECORDING_MIN_DURATION_MS).toBe(
      1000 / LIVE_TOOL_RECORDING_FPS,
    );
  });

  it("starts only after a structured live-preview frame arrives", () => {
    const recording: LiveToolRecording = {
      pushFrame: vi.fn(),
      stop: vi.fn(async () => {}),
    };
    const start = vi.fn(() => recording);
    const engine = new LiveToolRecordingEpisodeEngine(start);

    engine.handleSnapshot(undefined);
    engine.handleSnapshot(snapshot({ live: false }));
    engine.handleSnapshot(snapshot({ frameSeq: undefined, jpegB64: undefined }));
    expect(start).not.toHaveBeenCalled();

    engine.handleSnapshot(snapshot());
    expect(start).toHaveBeenCalledTimes(1);
    expect(start).toHaveBeenCalledWith({
      sessionId: "session-1",
      pageId: "page-1",
      assistantMessageId: "assistant-1",
      toolCallId: "call-1",
    });
    expect(recording.pushFrame).toHaveBeenCalledTimes(1);
  });

  it("uploads under the holder session when Live is mirrored onto the parent", () => {
    const recording: LiveToolRecording = {
      pushFrame: vi.fn(),
      stop: vi.fn(async () => {}),
    };
    const start = vi.fn(() => recording);
    const engine = new LiveToolRecordingEpisodeEngine(start);

    engine.handleSnapshot(
      snapshot({
        sessionId: "parent",
        holderSessionId: "child",
        parentSessionId: "parent",
      }),
    );
    expect(start).toHaveBeenCalledWith({
      sessionId: "child",
      pageId: "page-1",
      assistantMessageId: "assistant-1",
      toolCallId: "call-1",
    });
  });

  it("records each preview frame once and never restarts for non-frame events", () => {
    const recording: LiveToolRecording = {
      pushFrame: vi.fn(),
      stop: vi.fn(async () => {}),
    };
    const start = vi.fn(() => recording);
    const engine = new LiveToolRecordingEpisodeEngine(start);

    engine.handleSnapshot(snapshot());
    engine.handleSnapshot(snapshot({ seq: 3 }));
    engine.handleSnapshot(
      snapshot({ seq: 4, frameSeq: 4, jpegB64: "frame-two" }),
    );

    expect(start).toHaveBeenCalledTimes(1);
    expect(recording.pushFrame).toHaveBeenCalledTimes(2);
    expect(recording.stop).not.toHaveBeenCalled();
  });

  it("ends on detach and gives a new page exactly one new recorder", () => {
    const first: LiveToolRecording = {
      pushFrame: vi.fn(),
      stop: vi.fn(async () => {}),
    };
    const second: LiveToolRecording = {
      pushFrame: vi.fn(),
      stop: vi.fn(async () => {}),
    };
    const start = vi.fn()
      .mockReturnValueOnce(first)
      .mockReturnValueOnce(second);
    const engine = new LiveToolRecordingEpisodeEngine(start);

    engine.handleSnapshot(snapshot());
    engine.handleSnapshot(snapshot({ live: false, seq: 3 }));
    engine.handleSnapshot(
      snapshot({ pageId: "page-2", seq: 4, frameSeq: 4 }),
    );
    engine.handleSnapshot(snapshot({ pageId: "page-2", seq: 5, frameSeq: 4 }));

    expect(first.stop).toHaveBeenCalledTimes(1);
    expect(second.stop).not.toHaveBeenCalled();
    expect(start).toHaveBeenCalledTimes(2);

    engine.dispose();
    engine.dispose();
    expect(second.stop).toHaveBeenCalledTimes(1);
  });

  it("ends the old episode when the same page reanchors to a new invocation", () => {
    const first: LiveToolRecording = {
      pushFrame: vi.fn(),
      stop: vi.fn(async () => {}),
    };
    const second: LiveToolRecording = {
      pushFrame: vi.fn(),
      stop: vi.fn(async () => {}),
    };
    const start = vi.fn().mockReturnValueOnce(first).mockReturnValueOnce(second);
    const engine = new LiveToolRecordingEpisodeEngine(start);

    engine.handleSnapshot(snapshot());
    engine.handleSnapshot(snapshot({
      assistantMessageId: "assistant-2",
      toolCallId: "call-2",
      seq: 3,
    }));

    expect(first.stop).toHaveBeenCalledTimes(1);
    expect(start).toHaveBeenLastCalledWith({
      sessionId: "session-1",
      pageId: "page-1",
      assistantMessageId: "assistant-2",
      toolCallId: "call-2",
    });
  });
});
