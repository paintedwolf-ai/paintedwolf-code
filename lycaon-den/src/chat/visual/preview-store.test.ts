import { describe, expect, it, vi } from "vitest";
import type { PreviewEvent } from "../../api/types.ts";
import {
  acquirePreviewWatch,
  applyPreviewEvent,
  forgetLivePreviewSession,
  getLatestPreviewForHolder,
  getLivePreviewsForSession,
  getPreviewForInvocation,
  refreshLivePreviewSnapshots,
  resetLivePreviewStoreForTests,
} from "./preview-store.ts";

function event(overrides: Partial<PreviewEvent> = {}): PreviewEvent {
  return {
    op: "attach",
    session_id: "sess",
    page_id: "page-1",
    assistant_message_id: "assistant-1",
    tool_call_id: "call-1",
    seq: 1,
    ...overrides,
  };
}

describe("preview-store", () => {
  it("rehydrates invocation associations and mirrors worker pages to the parent", async () => {
    resetLivePreviewStoreForTests();
    const listSessionPreviews = vi.fn(async () => [
      {
        session_id: "child",
        parent_session_id: "parent",
        page_id: "page-1",
        assistant_message_id: "assistant-1",
        tool_call_id: "call-1",
        title: "Local app",
      },
    ]);

    await refreshLivePreviewSnapshots({ listSessionPreviews }, "parent");

    expect(getPreviewForInvocation("child", "assistant-1", "call-1")).toMatchObject({
      pageId: "page-1",
      live: true,
      title: "Local app",
      holderSessionId: "child",
    });
    expect(getLatestPreviewForHolder("parent", "child")).toMatchObject({
      holderSessionId: "child",
      assistantMessageId: "assistant-1",
    });
  });

  it("keeps the old invocation final and reanchors live to the next ordered call", () => {
    resetLivePreviewStoreForTests();
    applyPreviewEvent(event());
    applyPreviewEvent(event({
      op: "frame",
      seq: 2,
      jpeg_b64: "first-frame",
      mime: "image/jpeg",
    }));
    applyPreviewEvent(event({
      op: "state",
      seq: 3,
      assistant_message_id: "assistant-2",
      tool_call_id: "call-2",
      idle: false,
    }));

    expect(getPreviewForInvocation("sess", "assistant-1", "call-1")).toMatchObject({
      live: false,
      jpegB64: "first-frame",
    });
    expect(getPreviewForInvocation("sess", "assistant-2", "call-2")).toMatchObject({
      live: true,
      jpegB64: "first-frame",
      idle: false,
    });

    applyPreviewEvent(event({
      op: "detach",
      seq: 4,
      assistant_message_id: "assistant-2",
      tool_call_id: "call-2",
    }));
    expect(getPreviewForInvocation("sess", "assistant-2", "call-2")?.live).toBe(false);
  });

  it("tracks multiple held pages independently without moving frames between them", () => {
    resetLivePreviewStoreForTests();
    applyPreviewEvent(event({ op: "frame", jpeg_b64: "page-one" }));
    applyPreviewEvent(event({
      op: "frame",
      page_id: "page-2",
      assistant_message_id: "assistant-2",
      tool_call_id: "call-2",
      jpeg_b64: "page-two",
    }));

    expect(getPreviewForInvocation("sess", "assistant-1", "call-1")?.jpegB64).toBe("page-one");
    expect(getPreviewForInvocation("sess", "assistant-2", "call-2")?.jpegB64).toBe("page-two");
    expect(getLivePreviewsForSession("sess")).toHaveLength(2);
  });

  it("ignores stale events for one physical page", () => {
    resetLivePreviewStoreForTests();
    applyPreviewEvent(event({ op: "frame", seq: 4, jpeg_b64: "new" }));
    applyPreviewEvent(event({ op: "frame", seq: 3, jpeg_b64: "stale" }));
    expect(getPreviewForInvocation("sess", "assistant-1", "call-1")?.jpegB64).toBe("new");
  });

  it("refcounts watches per page", () => {
    resetLivePreviewStoreForTests();
    const watchPreview = vi.fn(async (
      _sessionId: string,
      req: { watching: boolean; page_id: string },
    ) => req);
    const releaseA = acquirePreviewWatch({ watchPreview }, "sess", "page-1");
    const releaseB = acquirePreviewWatch({ watchPreview }, "sess", "page-1");
    const releaseOther = acquirePreviewWatch({ watchPreview }, "sess", "page-2");

    expect(watchPreview).toHaveBeenCalledTimes(2);
    releaseA();
    expect(watchPreview).toHaveBeenCalledTimes(2);
    releaseB();
    releaseOther();
    expect(watchPreview).toHaveBeenCalledTimes(4);
  });

  it("bounds finished invocations per session while keeping every live page", () => {
    resetLivePreviewStoreForTests();
    let seq = 0;
    // Each iteration attaches a fresh page invocation then detaches it,
    // leaving a finished snapshot carrying its last frame.
    for (let i = 0; i < 80; i++) {
      applyPreviewEvent(event({
        op: "frame",
        page_id: `page-${i}`,
        assistant_message_id: `assistant-${i}`,
        tool_call_id: `call-${i}`,
        seq: ++seq,
        jpeg_b64: `frame-${i}`,
      }));
      applyPreviewEvent(event({
        op: "detach",
        page_id: `page-${i}`,
        assistant_message_id: `assistant-${i}`,
        tool_call_id: `call-${i}`,
        seq: ++seq,
      }));
    }
    applyPreviewEvent(event({
      op: "frame",
      page_id: "page-live",
      assistant_message_id: "assistant-live",
      tool_call_id: "call-live",
      seq: ++seq,
      jpeg_b64: "live-frame",
    }));

    // The oldest finished snapshots (and their frames) are gone.
    expect(
      getPreviewForInvocation("sess", "assistant-0", "call-0"),
    ).toBeUndefined();
    // The live page always survives.
    expect(
      getPreviewForInvocation("sess", "assistant-live", "call-live")?.live,
    ).toBe(true);
    // The newest finished snapshot is retained for review.
    expect(
      getPreviewForInvocation("sess", "assistant-79", "call-79")?.jpegB64,
    ).toBe("frame-79");
  });

  it("evicts whole sessions past the session cap, oldest first", () => {
    resetLivePreviewStoreForTests();
    let seq = 0;
    for (let i = 0; i < 70; i++) {
      applyPreviewEvent(event({
        session_id: `sess-${i}`,
        op: "frame",
        seq: ++seq,
        jpeg_b64: `frame-${i}`,
      }));
    }
    expect(
      getPreviewForInvocation("sess-0", "assistant-1", "call-1"),
    ).toBeUndefined();
    expect(
      getPreviewForInvocation("sess-69", "assistant-1", "call-1")?.jpegB64,
    ).toBe("frame-69");
  });

  it("forgets holder projections mirrored into a parent session", () => {
    resetLivePreviewStoreForTests();
    applyPreviewEvent(event({
      session_id: "child",
      parent_session_id: "parent",
      op: "frame",
      jpeg_b64: "frame",
    }));
    forgetLivePreviewSession("child");
    expect(getLivePreviewsForSession("child")).toHaveLength(0);
    expect(getLatestPreviewForHolder("parent", "child")).toBeUndefined();
  });
});
