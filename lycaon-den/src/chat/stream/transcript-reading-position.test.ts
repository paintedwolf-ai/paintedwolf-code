// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";

import { createTranscriptViewportController } from "./transcript-viewport.tsx";

import { persistTranscriptViewportSnapshot, transcriptViewportSnapshot } from "./transcript-viewport-state.ts";

import { beginShellLayoutBusy, endShellLayoutBusy, flushShellLayoutSettleForTests, resetShellLayoutBusyForTests } from "../../shell/shell-layout-busy.ts";

import { installViewportFixtureCleanup, streamFixture, followingFixture, rowsWindow, runtime } from "./transcript-viewport-test-fixture.ts";
installViewportFixtureCleanup();
describe("reading position", () => {
  it("restores a resident viewport after its inactive DOM loses the offset", () => {
    const f = followingFixture("s-resident");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-resident"));
    f.controller.stopFollowing();
    f.scrollTo(1_234);
    stop();
    f.scrollTo(0);
    f.controller.rowsChanged();

    const stopReopened = f.controller.bindRuntime(runtime("s-resident"));

    expect(f.stream.scrollTop).toBe(1_234);
    stopReopened();
    expect(transcriptViewportSnapshot("s-resident")?.position).toEqual({ rowKey: "r12", rowOffsetPx: 34 });
  });

  it("retains the saved row until the scroll extent is published", () => {
    persistTranscriptViewportSnapshot("s-extent", {
      position: { rowKey: "r9", rowOffsetPx: 10 }, openKeys: [],
    });
    const f = streamFixture({ content: 300, scrollTop: 0 });
    const controller = createTranscriptViewportController({ sessionId: () => "s-extent" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    controller.activateSession("s-extent");
    expect(f.stream.scrollTop).toBe(0);

    f.resize(2_000);
    controller.rowsChanged();

    expect(f.stream.scrollTop).toBe(910);
    expect(controller.following()).toBe(false);
  });

  it("saves where the reader is and restores it when the session reopens", () => {
    const first = followingFixture("s-read");
    first.controller.attachVirtualWindow(rowsWindow(first.stream, () => true));
    const stop = first.controller.bindRuntime(runtime("s-read"));
    first.controller.stopFollowing();
    first.scrollTo(1_234);
    stop();

    expect(transcriptViewportSnapshot("s-read")?.position).toEqual({
      rowKey: "r12",
      rowOffsetPx: 34,
    });

    const reopened = streamFixture({ scrollTop: 0 });
    const controller = createTranscriptViewportController({ sessionId: () => "s-read" });
    controller.attachStream(reopened.stream);
    controller.attachVirtualWindow(rowsWindow(reopened.stream, () => true));

    expect(controller.activateSession("s-read")).toBe(true);
    expect(controller.following()).toBe(false);
    expect(reopened.stream.scrollTop).toBe(1_234);
  });

  it("stores no position while following", () => {
    const f = followingFixture("s-follow-save");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    f.controller.saveSession();
    expect(transcriptViewportSnapshot("s-follow-save")).toMatchObject({ openKeys: [] });
    expect(transcriptViewportSnapshot("s-follow-save")?.position).toBeUndefined();
  });

  it("does not release following or record position 0 when shell layout is unstable or stream height is 0", () => {
    const f = followingFixture("s-unstable");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-unstable"));
    beginShellLayoutBusy();
    try {
      f.scrollTo(0);
      f.controller.saveSession();
      expect(f.controller.following()).toBe(true);
      expect(transcriptViewportSnapshot("s-unstable")?.position).toBeUndefined();
    } finally {
      endShellLayoutBusy();
      resetShellLayoutBusyForTests();
      stop();
    }
  });

  it("a reveal during unstable layout stops following at once and samples the place on settle", async () => {
    const f = followingFixture("s-reveal-unstable");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-reveal-unstable"));
    beginShellLayoutBusy();
    try {
      f.controller.ensureVisible(document.createElement("div"));
      expect(f.controller.following()).toBe(false);
      f.stream.scrollTop = 1_234;
    } finally {
      endShellLayoutBusy();
    }
    await flushShellLayoutSettleForTests();
    expect(f.controller.following()).toBe(false);
    expect(f.stream.scrollTop).toBe(1_234);
    f.controller.saveSession();
    expect(transcriptViewportSnapshot("s-reveal-unstable")?.position).toEqual({
      rowKey: "r12",
      rowOffsetPx: 34,
    });
    resetShellLayoutBusyForTests();
    stop();
  });

  it("repins tail on shell layout settle when following", async () => {
    const f = followingFixture("s-settle");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-settle"));
    beginShellLayoutBusy();
    f.scrollTo(100);
    endShellLayoutBusy();
    await flushShellLayoutSettleForTests();
    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(1_700);
    stop();
  });

  it("reaches the tail that grew under a modal once the modal closes", async () => {
    const geometryReports: ResizeObserverCallback[] = [];
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: ResizeObserverCallback) {
        geometryReports.push(callback);
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    });
    const f = followingFixture("s-modal");
    const stage = document.createElement("div");
    document.body.append(stage);
    stage.append(f.stream);
    const stop = f.controller.bindRuntime(runtime("s-modal"));
    // A modal makes the stage inert; the transcript keeps growing behind it.
    stage.setAttribute("inert", "");
    f.resize(2_400);
    for (const report of geometryReports) report([], {} as ResizeObserver);
    expect(f.stream.scrollTop).toBe(1_700);

    stage.removeAttribute("inert");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(f.controller.following()).toBe(true);
    expect(f.stream.scrollTop).toBe(2_100);
    stop();
  });

  it("does not resurrect a previous reading position when shell layout settles", async () => {
    const f = followingFixture("s-no-resurrect", { content: 2_000, scrollTop: 0 });
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    const stop = f.controller.bindRuntime(runtime("s-no-resurrect"));

    f.controller.stopFollowing();
    f.scrollTo(800);
    expect(f.stream.scrollTop).toBe(800);
    f.scrollTo(1_500);

    await flushShellLayoutSettleForTests();
    expect(f.stream.scrollTop).toBe(1_500);
    stop();
  });

  it("restores once the saved row arrives with the transcript", () => {
    persistTranscriptViewportSnapshot("s-hydrate", {
      position: { rowKey: "r9", rowOffsetPx: 10 },
      openKeys: [],
    });
    const f = streamFixture({ scrollTop: 0 });
    let loaded = false;
    const controller = createTranscriptViewportController({ sessionId: () => "s-hydrate" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(rowsWindow(f.stream, () => loaded));

    expect(controller.activateSession("s-hydrate")).toBe(true);
    expect(f.stream.scrollTop).toBe(0);

    loaded = true;
    controller.rowsChanged();
    expect(f.stream.scrollTop).toBe(910);
  });

  it("loads older history for a saved row, then restores it", async () => {
    persistTranscriptViewportSnapshot("s-history", {
      position: { rowKey: "r3", rowOffsetPx: 0 },
      openKeys: [],
    });
    const f = streamFixture({ scrollTop: 0 });
    let loaded = false;
    const ensureRowLoaded = vi.fn(async () => {
      loaded = true;
    });
    const controller = createTranscriptViewportController({ sessionId: () => "s-history" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(
      rowsWindow(f.stream, () => loaded, { ensureRowLoaded }),
    );
    controller.activateSession("s-history");
    expect(ensureRowLoaded).not.toHaveBeenCalled();

    const stop = controller.bindRuntime(runtime("s-history"));

    expect(ensureRowLoaded).toHaveBeenCalledWith("r3");
    await vi.waitFor(() => expect(f.stream.scrollTop).toBe(300));
    stop();
  });

  it("opens at the latest message when the saved row is gone", async () => {
    persistTranscriptViewportSnapshot("s-gone", {
      position: { rowKey: "r3", rowOffsetPx: 0 },
      openKeys: [],
    });
    const f = streamFixture({ scrollTop: 0 });
    const controller = createTranscriptViewportController({ sessionId: () => "s-gone" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(rowsWindow(f.stream, () => false));
    controller.activateSession("s-gone");

    const stop = controller.bindRuntime(runtime("s-gone"));

    await vi.waitFor(() => expect(controller.following()).toBe(true));
    expect(f.stream.scrollTop).toBe(1_700);
    stop();
  });

  it("drops a pending restore once the reader starts scrolling", async () => {
    persistTranscriptViewportSnapshot("s-reader-first", {
      position: { rowKey: "r3", rowOffsetPx: 0 },
      openKeys: [],
    });
    const f = streamFixture({ scrollTop: 0 });
    let loaded = false;
    let finishLoad!: () => void;
    const controller = createTranscriptViewportController({ sessionId: () => "s-reader-first" });
    controller.attachStream(f.stream);
    controller.attachVirtualWindow(rowsWindow(f.stream, () => loaded, {
      ensureRowLoaded: () => new Promise<void>((resolve) => {
        finishLoad = () => {
          loaded = true;
          resolve();
        };
      }),
    }));
    controller.activateSession("s-reader-first");
    const stop = controller.bindRuntime(runtime("s-reader-first"));

    f.stream.dispatchEvent(new WheelEvent("wheel", { bubbles: true, deltaY: 40 }));
    finishLoad();
    await Promise.resolve();
    await Promise.resolve();

    expect(f.stream.scrollTop).toBe(0);
    expect(controller.following()).toBe(false);
    stop();
  });

  it("does not carry a saved position into another session", () => {
    const f = followingFixture("s-old");
    f.controller.attachVirtualWindow(rowsWindow(f.stream, () => true));
    f.controller.stopFollowing();
    f.stream.scrollTop = 500;

    expect(f.controller.activateSession("s-new", "s-old")).toBe(false);

    expect(f.controller.following()).toBe(true);
    expect(transcriptViewportSnapshot("s-old")?.position).toEqual({
      rowKey: "r17",
      rowOffsetPx: 0,
    });
    expect(transcriptViewportSnapshot("s-new")).toBeUndefined();
  });
});
